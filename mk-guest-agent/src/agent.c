#include "mkga_agent.h"

#include <errno.h>
#include <stdio.h>
#include <string.h>

static const char *mkga_strerror_name(int err)
{
	switch (-err) {
	case EINVAL:
		return "invalid_argument";
	case ENOENT:
		return "not_found";
	case ENOSPC:
		return "resource_exhausted";
	case ENOSYS:
		return "not_implemented";
	default:
		return "runtime_error";
	}
}

void mkga_agent_init(struct mkga_agent *agent,
		     struct mkga_transport *transport,
		     struct mkga_runtime *runtime,
		     int receive_timeout_ms)
{
	memset(agent, 0, sizeof(*agent));
	agent->transport = transport;
	agent->runtime = runtime;
	agent->receive_timeout_ms = receive_timeout_ms;
}

int mkga_agent_handle(struct mkga_agent *agent,
		      const struct mkga_envelope *req,
		      struct mkga_envelope *resp)
{
	int rc;

	if (!agent || !agent->runtime || !req || !resp) {
		return -EINVAL;
	}

	if (req->kind != MKGA_MESSAGE_REQUEST) {
		mkga_envelope_set_error(req, resp, "invalid_kind",
				      "guest agent only accepts request messages");
		return 0;
	}

	switch (req->operation) {
	case MKGA_OP_CREATE_CONTAINER:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_create_container(
			agent->runtime,
			&req->payload.create_container_req,
			&resp->payload.create_container_resp);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "create_container failed");
		}
		return 0;

	case MKGA_OP_START_CONTAINER:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_start_container(
			agent->runtime,
			&req->payload.container_control_req);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "start_container failed");
		}
		return 0;

	case MKGA_OP_STOP_CONTAINER:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_stop_container(
			agent->runtime,
			&req->payload.container_control_req,
			&resp->payload.stop_container_resp);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "stop_container failed");
		}
		return 0;

	case MKGA_OP_REMOVE_CONTAINER:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_remove_container(
			agent->runtime,
			&req->payload.container_control_req);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "remove_container failed");
		}
			return 0;

	case MKGA_OP_STATUS_CONTAINER:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_status_container(
			agent->runtime,
			&req->payload.container_control_req,
			&resp->payload.container_status_resp);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "status_container failed");
		}
		return 0;

	case MKGA_OP_READ_LOG:
			mkga_envelope_make_response(req, resp);
			rc = mkga_runtime_read_log(
				agent->runtime,
				&req->payload.read_log_req,
			&resp->payload.read_log_resp);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "read_log failed");
			}
			return 0;

	case MKGA_OP_CONFIGURE_NETWORK:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_configure_network(
			agent->runtime,
			&req->payload.configure_network_req);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "configure_network failed");
		}
		return 0;

	case MKGA_OP_CONFIGURE_ENV:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_configure_env(
			agent->runtime,
			&req->payload.configure_env_req);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "configure_env failed");
		}
		return 0;

	case MKGA_OP_EXEC_TTY_PREPARE:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_exec_tty_prepare(
			agent->runtime,
			&req->payload.exec_tty_prepare_req,
			&resp->payload.exec_tty_prepare_resp);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "exec_tty_prepare failed");
		}
		return 0;

	case MKGA_OP_EXEC_TTY_START:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_exec_tty_start(
			agent->runtime,
			&req->payload.exec_session_control_req);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "exec_tty_start failed");
		}
		return 0;

	case MKGA_OP_EXEC_TTY_RESIZE:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_exec_tty_resize(
			agent->runtime,
			&req->payload.exec_tty_resize_req);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "exec_tty_resize failed");
		}
		return 0;

	case MKGA_OP_EXEC_TTY_CLOSE:
		mkga_envelope_make_response(req, resp);
		rc = mkga_runtime_exec_tty_close(
			agent->runtime,
			&req->payload.exec_session_control_req);
		if (rc != 0) {
			mkga_envelope_set_error(req, resp, mkga_strerror_name(rc),
					      "exec_tty_close failed");
		}
		return 0;

	default:
		{
			char message[MKGA_MAX_MESSAGE_LEN];

			(void)snprintf(message, sizeof(message),
				       "unsupported operation: %s",
				       mkga_operation_name(req->operation));
			mkga_envelope_set_error(req, resp, "unsupported_operation",
					      message);
		}
		return 0;
	}
}

int mkga_agent_serve(struct mkga_agent *agent, volatile sig_atomic_t *stop_flag)
{
	if (!agent || !agent->transport || !agent->runtime) {
		return -EINVAL;
	}

	while (!stop_flag || !*stop_flag) {
		struct mkga_envelope req;
		struct mkga_envelope resp;
		int rc;

		mkga_envelope_init(&req);
		rc = mkga_transport_receive(agent->transport, &req,
					    agent->receive_timeout_ms);
		if (rc == -ETIMEDOUT) {
			continue;
		}
		if (rc == -EINTR && stop_flag && *stop_flag) {
			return 0;
		}
		if (rc == -ESHUTDOWN && stop_flag && *stop_flag) {
			return 0;
		}
		if (rc != 0) {
			return rc;
		}

		mkga_envelope_init(&resp);
		rc = mkga_agent_handle(agent, &req, &resp);
		if (rc != 0) {
			return rc;
		}

		rc = mkga_transport_send(agent->transport, &resp);
		if (rc == -EINTR && stop_flag && *stop_flag) {
			return 0;
		}
		if (rc == -ESHUTDOWN && stop_flag && *stop_flag) {
			return 0;
		}
		if (rc != 0) {
			return rc;
		}
	}

	return 0;
}
