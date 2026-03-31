#include "mkga_transport.h"
#include "mkring_container.h"
#include "mkring_transport_uapi.h"

#include <errno.h>
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
 * Direct-entry transport backend.
 *
 * This backend keeps the existing mkga_transport_ops shape so the guest agent
 * above the transport layer can remain unchanged. The kernel syscall only
 * provides generic send/recv semantics; this file owns the control-channel
 * message encoding, decoding, ready publication, and response generation.
 *
 * This file is wired to the direct syscall entry. If the running userspace
 * headers do not expose __NR_mkring_transport yet, the raw wrapper returns
 * ENOSYS and transport creation fails fast.
 */

struct mkga_mkring_uapi_ops {
	int (*send)(void *impl, const struct mkring_transport_send *req);
	int (*recv)(void *impl, struct mkring_transport_recv *req);
	void (*destroy)(void *impl);
};

struct mkga_mkring_transport {
	uint16_t peer_kernel_id;
	char runtime_name[MKRING_CONTAINER_MAX_RUNTIME_NAME];
	uint32_t ready_features;
	const struct mkga_mkring_uapi_ops *uapi_ops;
	void *uapi_impl;
};

static void mkga_copy_string(char *dst, size_t dst_len, const char *src)
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

static void mkga_copy_bounded_string(char *dst, size_t dst_len,
				     const char *src, size_t src_len)
{
	size_t copy_len = 0;

	if (!dst || dst_len == 0) {
		return;
	}
	if (!src || src_len == 0) {
		dst[0] = '\0';
		return;
	}

	while (copy_len < src_len && src[copy_len] != '\0') {
		copy_len++;
	}
	if (copy_len >= dst_len) {
		copy_len = dst_len - 1U;
	}
	memcpy(dst, src, copy_len);
	dst[copy_len] = '\0';
}

static bool mkga_message_matches_operation(
	const struct mkring_container_message *msg)
{
	if (!msg) {
		return false;
	}

	switch (msg->hdr.operation) {
	case MKRING_CONTAINER_OP_CREATE:
		return msg->hdr.payload_len == sizeof(msg->payload.create_req);
	case MKRING_CONTAINER_OP_READ_LOG:
		return msg->hdr.payload_len == sizeof(msg->payload.read_log_req);
	case MKRING_CONTAINER_OP_EXEC_TTY_PREPARE:
		return msg->hdr.payload_len ==
		       sizeof(msg->payload.exec_tty_prepare_req);
	case MKRING_CONTAINER_OP_EXEC_TTY_START:
		return msg->hdr.payload_len ==
		       sizeof(msg->payload.exec_tty_start_req);
	case MKRING_CONTAINER_OP_EXEC_TTY_RESIZE:
		return msg->hdr.payload_len ==
		       sizeof(msg->payload.exec_tty_resize_req);
	case MKRING_CONTAINER_OP_EXEC_TTY_CLOSE:
		return msg->hdr.payload_len ==
		       sizeof(msg->payload.exec_tty_close_req);
	case MKRING_CONTAINER_OP_START:
	case MKRING_CONTAINER_OP_STOP:
	case MKRING_CONTAINER_OP_REMOVE:
	case MKRING_CONTAINER_OP_STATUS:
		return msg->hdr.payload_len == sizeof(msg->payload.control_req);
	default:
		return false;
	}
}

static int mkga_errno_from_code(const char *code)
{
	if (!code || code[0] == '\0') {
		return EIO;
	}
	if (strcmp(code, "invalid_argument") == 0) {
		return EINVAL;
	}
	if (strcmp(code, "not_found") == 0) {
		return ENOENT;
	}
	if (strcmp(code, "resource_exhausted") == 0) {
		return ENOSPC;
	}
	if (strcmp(code, "not_implemented") == 0) {
		return ENOSYS;
	}
	return EIO;
}

static enum mkga_operation mkga_operation_from_mkring(uint8_t operation)
{
	switch (operation) {
	case MKRING_CONTAINER_OP_CREATE:
		return MKGA_OP_CREATE_CONTAINER;
	case MKRING_CONTAINER_OP_START:
		return MKGA_OP_START_CONTAINER;
	case MKRING_CONTAINER_OP_STOP:
		return MKGA_OP_STOP_CONTAINER;
	case MKRING_CONTAINER_OP_REMOVE:
		return MKGA_OP_REMOVE_CONTAINER;
	case MKRING_CONTAINER_OP_STATUS:
		return MKGA_OP_STATUS_CONTAINER;
	case MKRING_CONTAINER_OP_READ_LOG:
		return MKGA_OP_READ_LOG;
	case MKRING_CONTAINER_OP_EXEC_TTY_PREPARE:
		return MKGA_OP_EXEC_TTY_PREPARE;
	case MKRING_CONTAINER_OP_EXEC_TTY_START:
		return MKGA_OP_EXEC_TTY_START;
	case MKRING_CONTAINER_OP_EXEC_TTY_RESIZE:
		return MKGA_OP_EXEC_TTY_RESIZE;
	case MKRING_CONTAINER_OP_EXEC_TTY_CLOSE:
		return MKGA_OP_EXEC_TTY_CLOSE;
	default:
		return MKGA_OP_INVALID;
	}
}

static uint8_t mkga_operation_to_mkring(enum mkga_operation operation)
{
	switch (operation) {
	case MKGA_OP_CREATE_CONTAINER:
		return MKRING_CONTAINER_OP_CREATE;
	case MKGA_OP_START_CONTAINER:
		return MKRING_CONTAINER_OP_START;
	case MKGA_OP_STOP_CONTAINER:
		return MKRING_CONTAINER_OP_STOP;
	case MKGA_OP_REMOVE_CONTAINER:
		return MKRING_CONTAINER_OP_REMOVE;
	case MKGA_OP_STATUS_CONTAINER:
		return MKRING_CONTAINER_OP_STATUS;
	case MKGA_OP_READ_LOG:
		return MKRING_CONTAINER_OP_READ_LOG;
	case MKGA_OP_EXEC_TTY_PREPARE:
		return MKRING_CONTAINER_OP_EXEC_TTY_PREPARE;
	case MKGA_OP_EXEC_TTY_START:
		return MKRING_CONTAINER_OP_EXEC_TTY_START;
	case MKGA_OP_EXEC_TTY_RESIZE:
		return MKRING_CONTAINER_OP_EXEC_TTY_RESIZE;
	case MKGA_OP_EXEC_TTY_CLOSE:
		return MKRING_CONTAINER_OP_EXEC_TTY_CLOSE;
	default:
		return MKRING_CONTAINER_OP_NONE;
	}
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

static int mkga_direct_send(void *impl, const struct mkring_transport_send *req)
{
	(void)impl;
	return mkga_mkring_transport_direct(MKRING_TRANSPORT_OP_SEND,
					    (void *)req);
}

static int mkga_direct_recv(void *impl, struct mkring_transport_recv *req)
{
	(void)impl;
	return mkga_mkring_transport_direct(MKRING_TRANSPORT_OP_RECV,
					    req);
}

static void mkga_direct_destroy(void *impl)
{
	(void)impl;
}

static const struct mkga_mkring_uapi_ops mkga_direct_uapi_ops = {
	.send = mkga_direct_send,
	.recv = mkga_direct_recv,
	.destroy = mkga_direct_destroy,
};

static int mkga_decode_request_message(
	const struct mkring_transport_recv *uapi_req,
	struct mkga_envelope *req)
{
	struct mkring_container_message msg;

	if (!uapi_req || !req) {
		return -EINVAL;
	}
	if (uapi_req->message_len != sizeof(msg)) {
		return -EINVAL;
	}
	memset(&msg, 0, sizeof(msg));
	memcpy(&msg, uapi_req->message, sizeof(msg));

	if (!mkring_proto_header_valid(&msg.hdr, sizeof(msg))) {
		return -EINVAL;
	}
	if (msg.hdr.channel != MKRING_CONTAINER_CHANNEL ||
	    msg.hdr.kind != MKRING_CONTAINER_KIND_REQUEST) {
		return -EINVAL;
	}
	if (!mkga_message_matches_operation(&msg)) {
		return -EINVAL;
	}

	mkga_envelope_init(req);
	req->kind = MKGA_MESSAGE_REQUEST;
	req->operation = mkga_operation_from_mkring(msg.hdr.operation);
	if (req->operation == MKGA_OP_INVALID) {
		return -EINVAL;
	}
	req->peer_kernel_id = uapi_req->peer_kernel_id;
	req->transport_request_id = msg.hdr.request_id;
	(void)snprintf(req->id, sizeof(req->id), "%llu",
		       (unsigned long long)msg.hdr.request_id);

	switch (req->operation) {
	case MKGA_OP_CREATE_CONTAINER:
		mkga_copy_bounded_string(req->kernel_id, sizeof(req->kernel_id),
					 msg.payload.create_req.kernel_id,
					 sizeof(msg.payload.create_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.create_container_req.kernel_id,
			sizeof(req->payload.create_container_req.kernel_id),
			msg.payload.create_req.kernel_id,
			sizeof(msg.payload.create_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.create_container_req.pod_id,
			sizeof(req->payload.create_container_req.pod_id),
			msg.payload.create_req.pod_id,
			sizeof(msg.payload.create_req.pod_id));
		mkga_copy_bounded_string(
			req->payload.create_container_req.name,
			sizeof(req->payload.create_container_req.name),
			msg.payload.create_req.name,
			sizeof(msg.payload.create_req.name));
		mkga_copy_bounded_string(
			req->payload.create_container_req.image,
			sizeof(req->payload.create_container_req.image),
			msg.payload.create_req.image,
			sizeof(msg.payload.create_req.image));
		mkga_copy_bounded_string(
			req->payload.create_container_req.log_path,
			sizeof(req->payload.create_container_req.log_path),
			msg.payload.create_req.log_path,
			sizeof(msg.payload.create_req.log_path));
		req->payload.create_container_req.argv_count =
			msg.payload.create_req.argv_count;
		if (req->payload.create_container_req.argv_count > MKGA_MAX_ARGV) {
			return -EINVAL;
		}
		for (size_t i = 0; i < req->payload.create_container_req.argv_count; i++) {
			mkga_copy_bounded_string(
				req->payload.create_container_req.argv[i],
				sizeof(req->payload.create_container_req.argv[i]),
				msg.payload.create_req.argv[i],
				sizeof(msg.payload.create_req.argv[i]));
		}
		return 0;

	case MKGA_OP_READ_LOG:
		mkga_copy_bounded_string(req->kernel_id, sizeof(req->kernel_id),
					 msg.payload.read_log_req.kernel_id,
					 sizeof(msg.payload.read_log_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.read_log_req.kernel_id,
			sizeof(req->payload.read_log_req.kernel_id),
			msg.payload.read_log_req.kernel_id,
			sizeof(msg.payload.read_log_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.read_log_req.container_id,
			sizeof(req->payload.read_log_req.container_id),
			msg.payload.read_log_req.container_id,
			sizeof(msg.payload.read_log_req.container_id));
		req->payload.read_log_req.offset = msg.payload.read_log_req.offset;
		req->payload.read_log_req.max_bytes =
			msg.payload.read_log_req.max_bytes;
		return 0;

	case MKGA_OP_EXEC_TTY_PREPARE:
		mkga_copy_bounded_string(
			req->kernel_id, sizeof(req->kernel_id),
			msg.payload.exec_tty_prepare_req.kernel_id,
			sizeof(msg.payload.exec_tty_prepare_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_tty_prepare_req.kernel_id,
			sizeof(req->payload.exec_tty_prepare_req.kernel_id),
			msg.payload.exec_tty_prepare_req.kernel_id,
			sizeof(msg.payload.exec_tty_prepare_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_tty_prepare_req.container_id,
			sizeof(req->payload.exec_tty_prepare_req.container_id),
			msg.payload.exec_tty_prepare_req.container_id,
			sizeof(msg.payload.exec_tty_prepare_req.container_id));
		req->payload.exec_tty_prepare_req.argv_count =
			msg.payload.exec_tty_prepare_req.argv_count;
		if (req->payload.exec_tty_prepare_req.argv_count > MKGA_MAX_ARGV) {
			return -EINVAL;
		}
		req->payload.exec_tty_prepare_req.tty =
			msg.payload.exec_tty_prepare_req.tty;
		req->payload.exec_tty_prepare_req.stdin_enabled =
			msg.payload.exec_tty_prepare_req.stdin_enabled;
		req->payload.exec_tty_prepare_req.stdout_enabled =
			msg.payload.exec_tty_prepare_req.stdout_enabled;
		req->payload.exec_tty_prepare_req.stderr_enabled =
			msg.payload.exec_tty_prepare_req.stderr_enabled;
		for (size_t i = 0; i < req->payload.exec_tty_prepare_req.argv_count; i++) {
			mkga_copy_bounded_string(
				req->payload.exec_tty_prepare_req.argv[i],
				sizeof(req->payload.exec_tty_prepare_req.argv[i]),
				msg.payload.exec_tty_prepare_req.argv[i],
				sizeof(msg.payload.exec_tty_prepare_req.argv[i]));
		}
		return 0;

	case MKGA_OP_EXEC_TTY_START:
		mkga_copy_bounded_string(
			req->kernel_id, sizeof(req->kernel_id),
			msg.payload.exec_tty_start_req.kernel_id,
			sizeof(msg.payload.exec_tty_start_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_session_control_req.kernel_id,
			sizeof(req->payload.exec_session_control_req.kernel_id),
			msg.payload.exec_tty_start_req.kernel_id,
			sizeof(msg.payload.exec_tty_start_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_session_control_req.session_id,
			sizeof(req->payload.exec_session_control_req.session_id),
			msg.payload.exec_tty_start_req.session_id,
			sizeof(msg.payload.exec_tty_start_req.session_id));
		return 0;

	case MKGA_OP_EXEC_TTY_RESIZE:
		mkga_copy_bounded_string(
			req->kernel_id, sizeof(req->kernel_id),
			msg.payload.exec_tty_resize_req.kernel_id,
			sizeof(msg.payload.exec_tty_resize_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_tty_resize_req.kernel_id,
			sizeof(req->payload.exec_tty_resize_req.kernel_id),
			msg.payload.exec_tty_resize_req.kernel_id,
			sizeof(msg.payload.exec_tty_resize_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_tty_resize_req.session_id,
			sizeof(req->payload.exec_tty_resize_req.session_id),
			msg.payload.exec_tty_resize_req.session_id,
			sizeof(msg.payload.exec_tty_resize_req.session_id));
		req->payload.exec_tty_resize_req.width =
			msg.payload.exec_tty_resize_req.width;
		req->payload.exec_tty_resize_req.height =
			msg.payload.exec_tty_resize_req.height;
		return 0;

	case MKGA_OP_EXEC_TTY_CLOSE:
		mkga_copy_bounded_string(
			req->kernel_id, sizeof(req->kernel_id),
			msg.payload.exec_tty_close_req.kernel_id,
			sizeof(msg.payload.exec_tty_close_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_session_control_req.kernel_id,
			sizeof(req->payload.exec_session_control_req.kernel_id),
			msg.payload.exec_tty_close_req.kernel_id,
			sizeof(msg.payload.exec_tty_close_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.exec_session_control_req.session_id,
			sizeof(req->payload.exec_session_control_req.session_id),
			msg.payload.exec_tty_close_req.session_id,
			sizeof(msg.payload.exec_tty_close_req.session_id));
		return 0;

	case MKGA_OP_START_CONTAINER:
	case MKGA_OP_STOP_CONTAINER:
	case MKGA_OP_REMOVE_CONTAINER:
	case MKGA_OP_STATUS_CONTAINER:
		mkga_copy_bounded_string(req->kernel_id, sizeof(req->kernel_id),
					 msg.payload.control_req.kernel_id,
					 sizeof(msg.payload.control_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.container_control_req.kernel_id,
			sizeof(req->payload.container_control_req.kernel_id),
			msg.payload.control_req.kernel_id,
			sizeof(msg.payload.control_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.container_control_req.container_id,
			sizeof(req->payload.container_control_req.container_id),
			msg.payload.control_req.container_id,
			sizeof(msg.payload.control_req.container_id));
		req->payload.container_control_req.timeout_millis =
			msg.payload.control_req.timeout_millis;
		return 0;

	default:
		return -EINVAL;
	}
}

static int mkga_encode_response_message(
	const struct mkga_envelope *resp,
	struct mkring_transport_send *uapi_resp)
{
	struct mkring_container_message msg;
	int mapped_errno = 0;

	if (!resp || !uapi_resp) {
		return -EINVAL;
	}
	if (resp->kind != MKGA_MESSAGE_RESPONSE) {
		return -EINVAL;
	}

	memset(uapi_resp, 0, sizeof(*uapi_resp));
	uapi_resp->peer_kernel_id = resp->peer_kernel_id;
	uapi_resp->channel = MKRING_CONTAINER_CHANNEL;

	memset(&msg, 0, sizeof(msg));
	msg.hdr.magic = MKRING_CONTAINER_MAGIC;
	msg.hdr.version = MKRING_CONTAINER_VERSION;
	msg.hdr.channel = MKRING_CONTAINER_CHANNEL;
	msg.hdr.kind = MKRING_CONTAINER_KIND_RESPONSE;
	msg.hdr.operation = mkga_operation_to_mkring(resp->operation);
	msg.hdr.request_id = resp->transport_request_id;
	if (msg.hdr.operation == MKRING_CONTAINER_OP_NONE ||
	    msg.hdr.request_id == 0) {
		return -EINVAL;
	}

	if (resp->error.present) {
		mapped_errno = mkga_errno_from_code(resp->error.code);
		msg.hdr.status = -mapped_errno;
		msg.hdr.payload_len = sizeof(msg.payload.error);
		msg.payload.error.errno_value = -mapped_errno;
		mkga_copy_string(msg.payload.error.message,
				 sizeof(msg.payload.error.message),
				 resp->error.message);
	} else {
		msg.hdr.status = 0;
		switch (resp->operation) {
		case MKGA_OP_CREATE_CONTAINER:
			msg.hdr.payload_len = sizeof(msg.payload.create_resp);
			mkga_copy_string(msg.payload.create_resp.container_id,
					 sizeof(msg.payload.create_resp.container_id),
					 resp->payload.create_container_resp.container_id);
			mkga_copy_string(msg.payload.create_resp.image_ref,
					 sizeof(msg.payload.create_resp.image_ref),
					 resp->payload.create_container_resp.image_ref);
			break;

		case MKGA_OP_START_CONTAINER:
		case MKGA_OP_REMOVE_CONTAINER:
		case MKGA_OP_EXEC_TTY_START:
		case MKGA_OP_EXEC_TTY_RESIZE:
		case MKGA_OP_EXEC_TTY_CLOSE:
			msg.hdr.payload_len = 0;
			break;

		case MKGA_OP_STOP_CONTAINER:
			msg.hdr.payload_len = sizeof(msg.payload.stop_resp);
			msg.payload.stop_resp.exit_code =
				resp->payload.stop_container_resp.exit_code;
			break;

		case MKGA_OP_STATUS_CONTAINER:
			msg.hdr.payload_len = sizeof(msg.payload.status_resp);
			msg.payload.status_resp.state =
				resp->payload.container_status_resp.state;
			msg.payload.status_resp.exit_code =
				resp->payload.container_status_resp.exit_code;
			msg.payload.status_resp.pid =
				resp->payload.container_status_resp.pid;
			msg.payload.status_resp.started_at_unix_nano =
				resp->payload.container_status_resp.started_at_unix_nano;
			msg.payload.status_resp.finished_at_unix_nano =
				resp->payload.container_status_resp.finished_at_unix_nano;
			mkga_copy_string(msg.payload.status_resp.message,
					 sizeof(msg.payload.status_resp.message),
					 resp->payload.container_status_resp.message);
			break;

		case MKGA_OP_READ_LOG:
			msg.hdr.payload_len = sizeof(msg.payload.read_log_resp);
			msg.payload.read_log_resp.next_offset =
				resp->payload.read_log_resp.next_offset;
			msg.payload.read_log_resp.data_len =
				resp->payload.read_log_resp.data_len;
			msg.payload.read_log_resp.eof =
				resp->payload.read_log_resp.eof ? 1U : 0U;
			memcpy(msg.payload.read_log_resp.data,
			       resp->payload.read_log_resp.data,
			       sizeof(resp->payload.read_log_resp.data));
			break;

		case MKGA_OP_EXEC_TTY_PREPARE:
			msg.hdr.payload_len =
				sizeof(msg.payload.exec_tty_prepare_resp);
			mkga_copy_string(
				msg.payload.exec_tty_prepare_resp.session_id,
				sizeof(msg.payload.exec_tty_prepare_resp.session_id),
				resp->payload.exec_tty_prepare_resp.session_id);
			break;

		default:
			return -EINVAL;
		}
	}

	uapi_resp->message_len = sizeof(msg);
	memcpy(uapi_resp->message, &msg, sizeof(msg));
	return 0;
}

static int mkga_encode_ready_message(
	const struct mkga_mkring_transport *impl,
	struct mkring_transport_send *uapi_req)
{
	struct mkring_container_message msg;

	if (!impl || !uapi_req) {
		return -EINVAL;
	}

	memset(uapi_req, 0, sizeof(*uapi_req));
	uapi_req->peer_kernel_id = impl->peer_kernel_id;
	uapi_req->channel = MKRING_CONTAINER_CHANNEL;

	memset(&msg, 0, sizeof(msg));
	msg.hdr.magic = MKRING_CONTAINER_MAGIC;
	msg.hdr.version = MKRING_CONTAINER_VERSION;
	msg.hdr.channel = MKRING_CONTAINER_CHANNEL;
	msg.hdr.kind = MKRING_CONTAINER_KIND_READY;
	msg.hdr.operation = MKRING_CONTAINER_OP_NONE;
	msg.hdr.request_id = 0;
	msg.hdr.status = 0;
	msg.hdr.payload_len = sizeof(msg.payload.ready);
	msg.payload.ready.features = impl->ready_features;
	mkga_copy_string(msg.payload.ready.runtime_name,
			 sizeof(msg.payload.ready.runtime_name),
			 impl->runtime_name);

	uapi_req->message_len = sizeof(msg);
	memcpy(uapi_req->message, &msg, sizeof(msg));
	return 0;
}

static int mkga_mkring_receive(struct mkga_transport *transport,
			       struct mkga_envelope *req,
			       int timeout_ms)
{
	struct mkga_mkring_transport *impl;
	struct mkring_transport_recv uapi_req;
	int rc;

	if (!transport || !transport->impl || !req) {
		return -EINVAL;
	}
	impl = transport->impl;
	if (!impl->uapi_ops || !impl->uapi_ops->recv) {
		return -ENOSYS;
	}

	memset(&uapi_req, 0, sizeof(uapi_req));
	uapi_req.channel = MKRING_CONTAINER_CHANNEL;
	uapi_req.timeout_ms = timeout_ms < 0 ? 0 : (uint32_t)timeout_ms;

	rc = impl->uapi_ops->recv(impl->uapi_impl, &uapi_req);
	if (rc != 0) {
		return rc;
	}
	return mkga_decode_request_message(&uapi_req, req);
}

static int mkga_mkring_send(struct mkga_transport *transport,
			    const struct mkga_envelope *resp)
{
	struct mkga_mkring_transport *impl;
	struct mkring_transport_send uapi_resp;
	int rc;

	if (!transport || !transport->impl || !resp) {
		return -EINVAL;
	}
	impl = transport->impl;
	if (!impl->uapi_ops || !impl->uapi_ops->send) {
		return -ENOSYS;
	}

	rc = mkga_encode_response_message(resp, &uapi_resp);
	if (rc != 0) {
		return rc;
	}

	rc = impl->uapi_ops->send(impl->uapi_impl, &uapi_resp);
	if (rc != 0) {
		return rc;
	}
	return 0;
}

static int mkga_mkring_publish_ready(struct mkga_mkring_transport *impl)
{
	struct mkring_transport_send uapi_req;
	int rc;

	if (!impl || !impl->uapi_ops || !impl->uapi_ops->send) {
		return -ENOSYS;
	}

	rc = mkga_encode_ready_message(impl, &uapi_req);
	if (rc != 0) {
		return rc;
	}
	rc = impl->uapi_ops->send(impl->uapi_impl, &uapi_req);
	if (rc != 0) {
		return rc;
	}
	return 0;
}

static void mkga_mkring_destroy(struct mkga_transport *transport)
{
	struct mkga_mkring_transport *impl;

	if (!transport) {
		return;
	}

	impl = transport->impl;
	if (impl && impl->uapi_ops && impl->uapi_ops->destroy) {
		impl->uapi_ops->destroy(impl->uapi_impl);
	}
	free(impl);
	free(transport);
}

static const struct mkga_transport_ops mkga_mkring_ops = {
	.receive = mkga_mkring_receive,
	.send = mkga_mkring_send,
	.destroy = mkga_mkring_destroy,
};

struct mkga_transport *mkga_mkring_transport_create(
	uint16_t peer_kernel_id,
	const char *runtime_name,
	uint32_t features)
{
	struct mkga_transport *transport;
	struct mkga_mkring_transport *impl;

	transport = calloc(1, sizeof(*transport));
	impl = calloc(1, sizeof(*impl));
	if (!transport || !impl) {
		free(transport);
		free(impl);
		errno = ENOMEM;
		return NULL;
	}

	impl->peer_kernel_id = peer_kernel_id;
	impl->ready_features = features;
	mkga_copy_string(impl->runtime_name,
			 sizeof(impl->runtime_name),
			 runtime_name ? runtime_name : "containerd");
	impl->uapi_ops = &mkga_direct_uapi_ops;
	impl->uapi_impl = impl;

	{
		int ready_rc = mkga_mkring_publish_ready(impl);
		if (ready_rc != 0) {
			free(transport);
			free(impl);
			errno = ready_rc < 0 ? -ready_rc : ready_rc;
			return NULL;
		}
	}

	transport->ops = &mkga_mkring_ops;
	transport->impl = impl;
	return transport;
}
