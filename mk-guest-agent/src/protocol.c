#include "mkga_protocol.h"

#include <stdio.h>
#include <string.h>
#include <sys/time.h>

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

int64_t mkga_now_millis(void)
{
	struct timeval tv;

	gettimeofday(&tv, NULL);
	return (int64_t)tv.tv_sec * 1000LL + (int64_t)(tv.tv_usec / 1000);
}

void mkga_envelope_init(struct mkga_envelope *env)
{
	if (!env) {
		return;
	}
	memset(env, 0, sizeof(*env));
	env->version = MKGA_VERSION;
	env->sent_at_millis = mkga_now_millis();
}

void mkga_envelope_make_response(const struct mkga_envelope *req,
				       struct mkga_envelope *resp)
{
	mkga_envelope_init(resp);
	if (!req || !resp) {
		return;
	}

	mkga_copy_string(resp->id, sizeof(resp->id), req->id);
	mkga_copy_string(resp->kernel_id, sizeof(resp->kernel_id), req->kernel_id);
	resp->kind = MKGA_MESSAGE_RESPONSE;
	resp->operation = req->operation;
	resp->peer_kernel_id = req->peer_kernel_id;
	resp->transport_request_id = req->transport_request_id;
}

void mkga_envelope_set_error(const struct mkga_envelope *req,
				  struct mkga_envelope *resp,
				  const char *code,
				  const char *message)
{
	mkga_envelope_make_response(req, resp);
	resp->error.present = true;
	mkga_copy_string(resp->error.code, sizeof(resp->error.code), code);
	mkga_copy_string(resp->error.message, sizeof(resp->error.message), message);
}

const char *mkga_operation_name(enum mkga_operation operation)
{
	switch (operation) {
	case MKGA_OP_CREATE_CONTAINER:
		return "create_container";
	case MKGA_OP_START_CONTAINER:
		return "start_container";
	case MKGA_OP_STOP_CONTAINER:
		return "stop_container";
	case MKGA_OP_REMOVE_CONTAINER:
		return "remove_container";
	case MKGA_OP_STATUS_CONTAINER:
		return "status_container";
	case MKGA_OP_READ_LOG:
		return "read_log";
	case MKGA_OP_EXEC_TTY_PREPARE:
		return "exec_tty_prepare";
	case MKGA_OP_EXEC_TTY_START:
		return "exec_tty_start";
	case MKGA_OP_EXEC_TTY_RESIZE:
		return "exec_tty_resize";
	case MKGA_OP_EXEC_TTY_CLOSE:
		return "exec_tty_close";
	default:
		return "invalid";
	}
}
