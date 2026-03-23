#include "mkga_transport.h"

#include "mkring_container.h"

#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

struct mkga_mkring_device_transport {
	int fd;
	uint16_t peer_kernel_id;
	char device_path[256];
};

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
	default:
		return MKRING_CONTAINER_OP_NONE;
	}
}

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

static bool mkga_message_matches_operation(const struct mkring_container_message *msg)
{
	if (!msg) {
		return false;
	}

	switch (msg->hdr.operation) {
	case MKRING_CONTAINER_OP_CREATE:
		return msg->hdr.payload_len == sizeof(msg->payload.create_req);
	case MKRING_CONTAINER_OP_READ_LOG:
		return msg->hdr.payload_len == sizeof(msg->payload.read_log_req);
	case MKRING_CONTAINER_OP_START:
	case MKRING_CONTAINER_OP_STOP:
	case MKRING_CONTAINER_OP_REMOVE:
	case MKRING_CONTAINER_OP_STATUS:
		return msg->hdr.payload_len == sizeof(msg->payload.control_req);
	default:
		return false;
	}
}

static int mkga_decode_request_packet(const struct mkring_container_packet *packet,
				      struct mkga_envelope *req)
{
	if (!packet || !req) {
		return -EINVAL;
	}
	if (packet->msg.hdr.magic != MKRING_CONTAINER_MAGIC ||
	    packet->msg.hdr.version != MKRING_CONTAINER_VERSION ||
	    packet->msg.hdr.channel != MKRING_CONTAINER_CHANNEL) {
		return -EINVAL;
	}
	if (packet->msg.hdr.kind != MKRING_CONTAINER_KIND_REQUEST) {
		return -EINVAL;
	}
	if (!mkga_message_matches_operation(&packet->msg)) {
		return -EINVAL;
	}

	mkga_envelope_init(req);
	req->kind = MKGA_MESSAGE_REQUEST;
	req->operation = mkga_operation_from_mkring(packet->msg.hdr.operation);
	if (req->operation == MKGA_OP_INVALID) {
		return -EINVAL;
	}
	req->peer_kernel_id = packet->peer_kernel_id;
	req->transport_request_id = packet->msg.hdr.request_id;
	(void)snprintf(req->id, sizeof(req->id), "%llu",
		       (unsigned long long)packet->msg.hdr.request_id);

	switch (req->operation) {
	case MKGA_OP_CREATE_CONTAINER:
		mkga_copy_bounded_string(req->kernel_id, sizeof(req->kernel_id),
					 packet->msg.payload.create_req.kernel_id,
					 sizeof(packet->msg.payload.create_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.create_container_req.kernel_id,
			sizeof(req->payload.create_container_req.kernel_id),
			packet->msg.payload.create_req.kernel_id,
			sizeof(packet->msg.payload.create_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.create_container_req.pod_id,
			sizeof(req->payload.create_container_req.pod_id),
			packet->msg.payload.create_req.pod_id,
			sizeof(packet->msg.payload.create_req.pod_id));
		mkga_copy_bounded_string(
			req->payload.create_container_req.name,
			sizeof(req->payload.create_container_req.name),
			packet->msg.payload.create_req.name,
			sizeof(packet->msg.payload.create_req.name));
		mkga_copy_bounded_string(
			req->payload.create_container_req.image,
			sizeof(req->payload.create_container_req.image),
			packet->msg.payload.create_req.image,
			sizeof(packet->msg.payload.create_req.image));
		mkga_copy_bounded_string(
			req->payload.create_container_req.log_path,
			sizeof(req->payload.create_container_req.log_path),
			packet->msg.payload.create_req.log_path,
			sizeof(packet->msg.payload.create_req.log_path));
		req->payload.create_container_req.argv_count =
			packet->msg.payload.create_req.argv_count;
		if (req->payload.create_container_req.argv_count > MKGA_MAX_ARGV) {
			return -EINVAL;
		}
		for (size_t i = 0; i < req->payload.create_container_req.argv_count; i++) {
			mkga_copy_bounded_string(
				req->payload.create_container_req.argv[i],
				sizeof(req->payload.create_container_req.argv[i]),
				packet->msg.payload.create_req.argv[i],
				sizeof(packet->msg.payload.create_req.argv[i]));
		}
		return 0;

	case MKGA_OP_READ_LOG:
		mkga_copy_bounded_string(req->kernel_id, sizeof(req->kernel_id),
					 packet->msg.payload.read_log_req.kernel_id,
					 sizeof(packet->msg.payload.read_log_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.read_log_req.kernel_id,
			sizeof(req->payload.read_log_req.kernel_id),
			packet->msg.payload.read_log_req.kernel_id,
			sizeof(packet->msg.payload.read_log_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.read_log_req.container_id,
			sizeof(req->payload.read_log_req.container_id),
			packet->msg.payload.read_log_req.container_id,
			sizeof(packet->msg.payload.read_log_req.container_id));
		req->payload.read_log_req.offset = packet->msg.payload.read_log_req.offset;
		req->payload.read_log_req.max_bytes = packet->msg.payload.read_log_req.max_bytes;
		return 0;

	case MKGA_OP_START_CONTAINER:
	case MKGA_OP_STOP_CONTAINER:
	case MKGA_OP_REMOVE_CONTAINER:
	case MKGA_OP_STATUS_CONTAINER:
		mkga_copy_bounded_string(req->kernel_id, sizeof(req->kernel_id),
					 packet->msg.payload.control_req.kernel_id,
					 sizeof(packet->msg.payload.control_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.container_control_req.kernel_id,
			sizeof(req->payload.container_control_req.kernel_id),
			packet->msg.payload.control_req.kernel_id,
			sizeof(packet->msg.payload.control_req.kernel_id));
		mkga_copy_bounded_string(
			req->payload.container_control_req.container_id,
			sizeof(req->payload.container_control_req.container_id),
			packet->msg.payload.control_req.container_id,
			sizeof(packet->msg.payload.control_req.container_id));
		req->payload.container_control_req.timeout_millis =
			packet->msg.payload.control_req.timeout_millis;
		return 0;

	default:
		return -EINVAL;
	}
}

static int mkga_encode_response_packet(const struct mkga_envelope *resp,
				       struct mkring_container_packet *packet)
{
	int mapped_errno = 0;

	if (!resp || !packet) {
		return -EINVAL;
	}
	if (resp->kind != MKGA_MESSAGE_RESPONSE) {
		return -EINVAL;
	}

	memset(packet, 0, sizeof(*packet));
	packet->peer_kernel_id = resp->peer_kernel_id;
	packet->msg.hdr.magic = MKRING_CONTAINER_MAGIC;
	packet->msg.hdr.version = MKRING_CONTAINER_VERSION;
	packet->msg.hdr.channel = MKRING_CONTAINER_CHANNEL;
	packet->msg.hdr.kind = MKRING_CONTAINER_KIND_RESPONSE;
	packet->msg.hdr.operation = mkga_operation_to_mkring(resp->operation);
	packet->msg.hdr.request_id = resp->transport_request_id;

	if (packet->msg.hdr.operation == MKRING_CONTAINER_OP_NONE ||
	    packet->msg.hdr.request_id == 0) {
		return -EINVAL;
	}

	if (resp->error.present) {
		mapped_errno = mkga_errno_from_code(resp->error.code);
		packet->msg.hdr.status = -mapped_errno;
		packet->msg.hdr.payload_len = sizeof(packet->msg.payload.error);
		packet->msg.payload.error.errno_value = -mapped_errno;
		mkga_copy_string(packet->msg.payload.error.message,
				 sizeof(packet->msg.payload.error.message),
				 resp->error.message);
		return 0;
	}

	packet->msg.hdr.status = 0;
	switch (resp->operation) {
	case MKGA_OP_CREATE_CONTAINER:
		packet->msg.hdr.payload_len = sizeof(packet->msg.payload.create_resp);
		mkga_copy_string(packet->msg.payload.create_resp.container_id,
				 sizeof(packet->msg.payload.create_resp.container_id),
				 resp->payload.create_container_resp.container_id);
		mkga_copy_string(packet->msg.payload.create_resp.image_ref,
				 sizeof(packet->msg.payload.create_resp.image_ref),
				 resp->payload.create_container_resp.image_ref);
		return 0;

		case MKGA_OP_START_CONTAINER:
		case MKGA_OP_REMOVE_CONTAINER:
			packet->msg.hdr.payload_len = 0;
			return 0;

		case MKGA_OP_STOP_CONTAINER:
			packet->msg.hdr.payload_len = sizeof(packet->msg.payload.stop_resp);
			packet->msg.payload.stop_resp.exit_code =
				resp->payload.stop_container_resp.exit_code;
			return 0;

		case MKGA_OP_STATUS_CONTAINER:
			packet->msg.hdr.payload_len = sizeof(packet->msg.payload.status_resp);
			packet->msg.payload.status_resp.state =
				resp->payload.container_status_resp.state;
			packet->msg.payload.status_resp.exit_code =
				resp->payload.container_status_resp.exit_code;
			packet->msg.payload.status_resp.pid =
				resp->payload.container_status_resp.pid;
			packet->msg.payload.status_resp.started_at_unix_nano =
				resp->payload.container_status_resp.started_at_unix_nano;
			packet->msg.payload.status_resp.finished_at_unix_nano =
				resp->payload.container_status_resp.finished_at_unix_nano;
			mkga_copy_string(packet->msg.payload.status_resp.message,
					 sizeof(packet->msg.payload.status_resp.message),
					 resp->payload.container_status_resp.message);
			return 0;

		case MKGA_OP_READ_LOG:
			packet->msg.hdr.payload_len = sizeof(packet->msg.payload.read_log_resp);
			packet->msg.payload.read_log_resp.next_offset =
				resp->payload.read_log_resp.next_offset;
			packet->msg.payload.read_log_resp.data_len =
				resp->payload.read_log_resp.data_len;
			packet->msg.payload.read_log_resp.eof =
				resp->payload.read_log_resp.eof ? 1U : 0U;
			memcpy(packet->msg.payload.read_log_resp.data,
			       resp->payload.read_log_resp.data,
			       sizeof(resp->payload.read_log_resp.data));
			return 0;

		default:
			return -EINVAL;
		}
}

static int mkga_device_receive(struct mkga_transport *transport,
			       struct mkga_envelope *req,
			       int timeout_ms)
{
	struct mkga_mkring_device_transport *impl;
	struct pollfd pfd;
	struct mkring_container_packet packet;
	ssize_t nread;
	int rc;

	if (!transport || !transport->impl || !req) {
		return -EINVAL;
	}
	impl = transport->impl;

	memset(&pfd, 0, sizeof(pfd));
	pfd.fd = impl->fd;
	pfd.events = POLLIN;

	rc = poll(&pfd, 1, timeout_ms < 0 ? -1 : timeout_ms);
	if (rc == 0) {
		return -ETIMEDOUT;
	}
	if (rc < 0) {
		return -errno;
	}
	if ((pfd.revents & (POLLERR | POLLHUP | POLLNVAL)) != 0) {
		return -ESHUTDOWN;
	}
	if ((pfd.revents & POLLIN) == 0) {
		return -EIO;
	}

	memset(&packet, 0, sizeof(packet));
	nread = read(impl->fd, &packet, sizeof(packet));
	if (nread < 0) {
		return -errno;
	}
	if ((size_t)nread != sizeof(packet)) {
		return -EIO;
	}

	return mkga_decode_request_packet(&packet, req);
}

static int mkga_device_send(struct mkga_transport *transport,
			    const struct mkga_envelope *resp)
{
	struct mkga_mkring_device_transport *impl;
	struct mkring_container_packet packet;
	ssize_t nwritten;
	int rc;

	if (!transport || !transport->impl || !resp) {
		return -EINVAL;
	}
	impl = transport->impl;
	if (resp->peer_kernel_id != impl->peer_kernel_id) {
		return -EINVAL;
	}

	rc = mkga_encode_response_packet(resp, &packet);
	if (rc != 0) {
		return rc;
	}

	nwritten = write(impl->fd, &packet, sizeof(packet));
	if (nwritten < 0) {
		return -errno;
	}
	if ((size_t)nwritten != sizeof(packet)) {
		return -EIO;
	}
	return 0;
}

static void mkga_device_destroy(struct mkga_transport *transport)
{
	struct mkga_mkring_device_transport *impl;

	if (!transport) {
		return;
	}
	impl = transport->impl;
	if (impl) {
		if (impl->fd >= 0) {
			(void)close(impl->fd);
		}
		free(impl);
	}
	free(transport);
}

static const struct mkga_transport_ops mkga_device_ops = {
	.receive = mkga_device_receive,
	.send = mkga_device_send,
	.destroy = mkga_device_destroy,
};

struct mkga_transport *mkga_mkring_device_transport_create(
	const char *device_path,
	uint16_t peer_kernel_id,
	const char *runtime_name,
	uint32_t features)
{
	struct mkga_transport *transport = NULL;
	struct mkga_mkring_device_transport *impl = NULL;
	struct mkring_container_set_ready ready;
	const char *path = device_path ? device_path : "/dev/mkring_container_bridge";
	const char *runtime = runtime_name ? runtime_name : "containerd";
	int fd;

	fd = open(path, O_RDWR);
	if (fd < 0) {
		return NULL;
	}

	transport = calloc(1, sizeof(*transport));
	impl = calloc(1, sizeof(*impl));
	if (!transport || !impl) {
		(void)close(fd);
		free(transport);
		free(impl);
		errno = ENOMEM;
		return NULL;
	}

	memset(&ready, 0, sizeof(ready));
	ready.peer_kernel_id = peer_kernel_id;
	ready.features = features;
	mkga_copy_string(ready.runtime_name, sizeof(ready.runtime_name), runtime);
	if (ioctl(fd, MKRING_CONTAINER_IOC_SET_READY, &ready) != 0) {
		int saved_errno = errno;
		(void)close(fd);
		free(transport);
		free(impl);
		errno = saved_errno;
		return NULL;
	}

	memset(impl, 0, sizeof(*impl));
	impl->fd = fd;
	impl->peer_kernel_id = peer_kernel_id;
	mkga_copy_string(impl->device_path, sizeof(impl->device_path), path);

	transport->ops = &mkga_device_ops;
	transport->impl = impl;
	return transport;
}
