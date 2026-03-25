#include "mkga_stream.h"

#include "mkring_stream.h"
#include "session.h"

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

struct mkga_stream_state {
	int fd;
	uint16_t peer_kernel_id;
	char device_path[256];
	pthread_t reader_thread;
	pthread_mutex_t lock;
	bool started;
	bool stopping;
	uint64_t next_seq;
};

static struct mkga_stream_state mkga_stream = {
	.fd = -1,
	.peer_kernel_id = 0,
	.device_path = "/dev/mkring_stream_bridge",
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

static int mkga_stream_write_packet_locked(const struct mkring_stream_packet *packet)
{
	ssize_t nwritten;

	nwritten = write(mkga_stream.fd, packet, sizeof(*packet));
	if (nwritten < 0) {
		return -errno;
	}
	if ((size_t)nwritten != sizeof(*packet)) {
		return -EIO;
	}
	return 0;
}

static int mkga_stream_send_packet(uint8_t stream_type, const char *session_id,
				   const uint8_t *data, size_t len)
{
	struct mkring_stream_packet packet;
	int rc;

	if (!session_id || len > MKRING_STREAM_MAX_PAYLOAD) {
		return -EINVAL;
	}

	memset(&packet, 0, sizeof(packet));
	packet.peer_kernel_id = mkga_stream.peer_kernel_id;
	packet.msg.hdr.magic = MKRING_STREAM_MAGIC;
	packet.msg.hdr.version = MKRING_STREAM_VERSION;
	packet.msg.hdr.channel = MKRING_STREAM_CHANNEL;
	packet.msg.hdr.stream_type = stream_type;
	packet.msg.hdr.payload_len = (uint32_t)len;
	mkga_stream_copy_string(packet.msg.hdr.session_id,
				sizeof(packet.msg.hdr.session_id), session_id);
	if (data && len > 0) {
		memcpy(packet.msg.payload, data, len);
	}

	pthread_mutex_lock(&mkga_stream.lock);
	packet.msg.hdr.session_seq = ++mkga_stream.next_seq;
	rc = mkga_stream_write_packet_locked(&packet);
	pthread_mutex_unlock(&mkga_stream.lock);
	return rc;
}

static int mkga_stream_handle_packet(const struct mkring_stream_packet *packet)
{
	if (!packet) {
		return -EINVAL;
	}
	if (packet->msg.hdr.magic != MKRING_STREAM_MAGIC ||
	    packet->msg.hdr.version != MKRING_STREAM_VERSION ||
	    packet->msg.hdr.channel != MKRING_STREAM_CHANNEL ||
	    packet->msg.hdr.payload_len > MKRING_STREAM_MAX_PAYLOAD) {
		return -EINVAL;
	}

	switch (packet->msg.hdr.stream_type) {
	case MKRING_STREAM_TYPE_STDIN:
		(void)fprintf(stderr,
			      "mkga stream recv stdin session=%s peer_kernel_id=%u bytes=%u\n",
			      packet->msg.hdr.session_id,
			      packet->peer_kernel_id,
			      packet->msg.hdr.payload_len);
		return mkga_session_write_stdin(packet->msg.hdr.session_id,
						packet->msg.payload,
						packet->msg.hdr.payload_len);
	default:
		return 0;
	}
}

static void *mkga_stream_reader(void *arg)
{
	(void)arg;

	for (;;) {
		struct mkring_stream_packet packet;
		ssize_t nread;

		nread = read(mkga_stream.fd, &packet, sizeof(packet));
		if (nread < 0) {
			if (errno == EINTR) {
				continue;
			}
			return NULL;
		}
		if ((size_t)nread != sizeof(packet) || mkga_stream.stopping) {
			return NULL;
		}
		(void)mkga_stream_handle_packet(&packet);
	}
}

int mkga_stream_ensure_started(void)
{
	const char *device_path;
	int fd;
	int rc;

	pthread_mutex_lock(&mkga_stream.lock);
	if (mkga_stream.started) {
		pthread_mutex_unlock(&mkga_stream.lock);
		return 0;
	}

	device_path = getenv("MK_GUEST_AGENT_STREAM_DEVICE");
	mkga_stream.peer_kernel_id =
		mkga_stream_getenv_u16("MK_GUEST_AGENT_PEER_KERNEL_ID", 0);
	mkga_stream_copy_string(mkga_stream.device_path, sizeof(mkga_stream.device_path),
				device_path ? device_path : "/dev/mkring_stream_bridge");

	fd = open(mkga_stream.device_path, O_RDWR);
	if (fd < 0) {
		rc = -errno;
		(void)fprintf(stderr,
			      "mkga stream open failed path=%s err=%d\n",
			      mkga_stream.device_path,
			      errno);
		pthread_mutex_unlock(&mkga_stream.lock);
		return rc;
	}
	mkga_stream.fd = fd;
	mkga_stream.stopping = false;
	mkga_stream.next_seq = 0;
	rc = pthread_create(&mkga_stream.reader_thread, NULL, mkga_stream_reader, NULL);
	if (rc != 0) {
		(void)close(fd);
		mkga_stream.fd = -1;
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
