#include "mkga_stream.h"

#include "mkring_stream.h"
#include "mkring_transport_uapi.h"
#include "session.h"

#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#ifdef __linux__
#include <sys/syscall.h>
#endif

/*
 * Stream dataplane backend.
 *
 * The public mkga_stream_* API stays unchanged so the guest runtime code does
 * not need to change. Internally this uses the generic mkring transport
 * syscall on channel 3.
 */

enum {
	MKGA_STREAM_RECV_TIMEOUT_MS = 1000,
};

struct mkga_stream_state {
	uint16_t peer_kernel_id;
	pthread_t reader_thread;
	pthread_mutex_t lock;
	bool started;
	bool stopping;
	uint64_t next_seq;
};

static struct mkga_stream_state mkga_stream = {
	.peer_kernel_id = 0,
	.lock = PTHREAD_MUTEX_INITIALIZER,
};

static void mkga_stream_copy_string(char *dst, size_t dst_len, const char *src)
{
	size_t copy_len;

	if (!dst || dst_len == 0) {
		return;
	}
	if (!src) {
		dst[0] = '\0';
		return;
	}
	copy_len = strlen(src);
	if (copy_len >= dst_len) {
		copy_len = dst_len - 1U;
	}
	memcpy(dst, src, copy_len);
	dst[copy_len] = '\0';
}

static uint16_t mkga_stream_getenv_u16(const char *key, uint16_t fallback)
{
	const char *value = getenv(key);
	char *end = NULL;
	unsigned long parsed;

	if (!value || value[0] == '\0') {
		return fallback;
	}
	parsed = strtoul(value, &end, 10);
	if (!end || *end != '\0' || parsed > 65535UL) {
		return fallback;
	}
	return (uint16_t)parsed;
}

static int mkga_mkring_transport_direct(uint32_t op, void *arg)
{
#if defined(__linux__) && defined(__NR_mkring_transport)
	long rc = syscall(__NR_mkring_transport, (long)op, arg);

	if (rc == -1) {
		return -errno;
	}
	return (int)rc;
#else
	(void)op;
	(void)arg;
	return -ENOSYS;
#endif
}

static int mkga_stream_message_valid(const struct mkring_stream_message *msg)
{
	if (!msg) {
		return 0;
	}
	if (msg->hdr.magic != MKRING_STREAM_MAGIC ||
	    msg->hdr.version != MKRING_STREAM_VERSION ||
	    msg->hdr.channel != MKRING_STREAM_CHANNEL ||
	    msg->hdr.payload_len > MKRING_STREAM_MAX_PAYLOAD) {
		return 0;
	}

	switch (msg->hdr.stream_type) {
	case MKRING_STREAM_TYPE_STDIN:
	case MKRING_STREAM_TYPE_OUTPUT:
	case MKRING_STREAM_TYPE_CONTROL:
		return 1;
	default:
		return 0;
	}
}

static int mkga_stream_send_packet(uint8_t stream_type, const char *session_id,
				   const uint8_t *data, size_t len)
{
	struct mkring_transport_send req;
	struct mkring_stream_message msg;
	int rc;

	if (!session_id || len > MKRING_STREAM_MAX_PAYLOAD) {
		return -EINVAL;
	}

	memset(&req, 0, sizeof(req));
	req.peer_kernel_id = mkga_stream.peer_kernel_id;
	req.channel = MKRING_STREAM_CHANNEL;

	memset(&msg, 0, sizeof(msg));
	msg.hdr.magic = MKRING_STREAM_MAGIC;
	msg.hdr.version = MKRING_STREAM_VERSION;
	msg.hdr.channel = MKRING_STREAM_CHANNEL;
	msg.hdr.stream_type = stream_type;
	msg.hdr.payload_len = (uint32_t)len;
	mkga_stream_copy_string(msg.hdr.session_id,
				sizeof(msg.hdr.session_id), session_id);
	if (data && len > 0) {
		memcpy(msg.payload, data, len);
	}

	pthread_mutex_lock(&mkga_stream.lock);
	msg.hdr.session_seq = ++mkga_stream.next_seq;
	req.message_len = sizeof(msg);
	memcpy(req.message, &msg, sizeof(msg));
	rc = mkga_mkring_transport_direct(MKRING_TRANSPORT_OP_SEND, &req);
	pthread_mutex_unlock(&mkga_stream.lock);
	return rc;
}

static int mkga_stream_handle_message(uint16_t peer_kernel_id,
				      const struct mkring_stream_message *msg)
{
	if (!msg) {
		return -EINVAL;
	}
	if (!mkga_stream_message_valid(msg)) {
		return -EINVAL;
	}

	switch (msg->hdr.stream_type) {
	case MKRING_STREAM_TYPE_STDIN:
		(void)fprintf(stderr,
			      "mkga stream recv stdin session=%s peer_kernel_id=%u bytes=%u\n",
			      msg->hdr.session_id,
			      peer_kernel_id,
			      msg->hdr.payload_len);
		return mkga_session_write_stdin(msg->hdr.session_id,
						msg->payload,
						msg->hdr.payload_len);
	default:
		return 0;
	}
}

static void *mkga_stream_reader(void *arg)
{
	(void)arg;

	for (;;) {
		struct mkring_transport_recv req;
		struct mkring_stream_message msg;
		int rc;

		memset(&req, 0, sizeof(req));
		req.channel = MKRING_STREAM_CHANNEL;
		req.timeout_ms = MKGA_STREAM_RECV_TIMEOUT_MS;

		rc = mkga_mkring_transport_direct(MKRING_TRANSPORT_OP_RECV, &req);
		if (rc != 0) {
			if (rc == -EINTR || rc == -ETIMEDOUT || rc == -EAGAIN) {
				if (mkga_stream.stopping) {
					return NULL;
				}
				continue;
			}
			(void)fprintf(stderr,
				      "mkga stream recv failed rc=%d channel=%u\n",
				      rc,
				      MKRING_STREAM_CHANNEL);
			return NULL;
		}
		if (mkga_stream.stopping) {
			return NULL;
		}
		if (req.message_len != sizeof(msg)) {
			continue;
		}

		memset(&msg, 0, sizeof(msg));
		memcpy(&msg, req.message, sizeof(msg));
		(void)mkga_stream_handle_message(req.peer_kernel_id, &msg);
	}
}

int mkga_stream_ensure_started(void)
{
	int rc;

	pthread_mutex_lock(&mkga_stream.lock);
	if (mkga_stream.started) {
		pthread_mutex_unlock(&mkga_stream.lock);
		return 0;
	}

	mkga_stream.peer_kernel_id =
		mkga_stream_getenv_u16("MK_GUEST_AGENT_PEER_KERNEL_ID", 0);
	mkga_stream.stopping = false;
	mkga_stream.next_seq = 0;
	rc = pthread_create(&mkga_stream.reader_thread, NULL, mkga_stream_reader, NULL);
	if (rc != 0) {
		pthread_mutex_unlock(&mkga_stream.lock);
		return -rc;
	}
	(void)pthread_detach(mkga_stream.reader_thread);
	mkga_stream.started = true;
	pthread_mutex_unlock(&mkga_stream.lock);
	return 0;
}

int mkga_stream_send_output(const char *session_id, const uint8_t *data, size_t len)
{
	int rc;

	rc = mkga_stream_ensure_started();
	if (rc != 0) {
		return rc;
	}
	(void)fprintf(stderr,
		      "mkga stream send output session=%s peer_kernel_id=%u bytes=%zu magic=0x%x version=%u channel=%u stream_type=%u payload_len=%u\n",
		      session_id ? session_id : "<nil>",
		      mkga_stream.peer_kernel_id,
		      len,
		      MKRING_STREAM_MAGIC,
		      MKRING_STREAM_VERSION,
		      MKRING_STREAM_CHANNEL,
		      MKRING_STREAM_TYPE_OUTPUT,
		      (unsigned)len);
	rc = mkga_stream_send_packet(MKRING_STREAM_TYPE_OUTPUT, session_id, data, len);
	(void)fprintf(stderr,
		      "mkga stream send output session=%s rc=%d\n",
		      session_id ? session_id : "<nil>",
		      rc);
	return rc;
}

int mkga_stream_send_exit(const char *session_id, int32_t exit_code)
{
	struct mkring_stream_control_exit payload;
	int rc;

	rc = mkga_stream_ensure_started();
	if (rc != 0) {
		return rc;
	}

	memset(&payload, 0, sizeof(payload));
	payload.kind = MKRING_STREAM_CTL_EXIT;
	payload.exit_code = exit_code;
	(void)fprintf(stderr,
		      "mkga stream send exit session=%s peer_kernel_id=%u exit_code=%d magic=0x%x version=%u channel=%u stream_type=%u payload_len=%zu\n",
		      session_id ? session_id : "<nil>",
		      mkga_stream.peer_kernel_id,
		      exit_code,
		      MKRING_STREAM_MAGIC,
		      MKRING_STREAM_VERSION,
		      MKRING_STREAM_CHANNEL,
		      MKRING_STREAM_TYPE_CONTROL,
		      sizeof(payload));
	rc = mkga_stream_send_packet(MKRING_STREAM_TYPE_CONTROL, session_id,
				     (const uint8_t *)&payload, sizeof(payload));
	(void)fprintf(stderr,
		      "mkga stream send exit session=%s rc=%d\n",
		      session_id ? session_id : "<nil>",
		      rc);
	return rc;
}
