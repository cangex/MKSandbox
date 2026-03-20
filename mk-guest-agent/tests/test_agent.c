#include "mkga_agent.h"

#include <assert.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>

struct test_thread_ctx {
	struct mkga_agent *agent;
	volatile sig_atomic_t *stop_flag;
	int rc;
};

static void *run_agent(void *arg)
{
	struct test_thread_ctx *ctx = arg;

	ctx->rc = mkga_agent_serve(ctx->agent, ctx->stop_flag);
	return NULL;
}

static void make_create_request(struct mkga_envelope *req, const char *request_id)
{
	mkga_envelope_init(req);
	(void)snprintf(req->id, sizeof(req->id), "%s", request_id);
	req->kind = MKGA_MESSAGE_REQUEST;
	req->operation = MKGA_OP_CREATE_CONTAINER;
	req->peer_kernel_id = 7;
	(void)snprintf(req->kernel_id, sizeof(req->kernel_id), "%s", "kernel-a");
	(void)snprintf(req->payload.create_container_req.kernel_id,
		       sizeof(req->payload.create_container_req.kernel_id),
		       "%s", "kernel-a");
	(void)snprintf(req->payload.create_container_req.pod_id,
		       sizeof(req->payload.create_container_req.pod_id),
		       "%s", "pod-1");
	(void)snprintf(req->payload.create_container_req.name,
		       sizeof(req->payload.create_container_req.name),
		       "%s", "ctr");
	(void)snprintf(req->payload.create_container_req.image,
		       sizeof(req->payload.create_container_req.image),
		       "%s", "busybox");
}

static void make_control_request(struct mkga_envelope *req,
				 const char *request_id,
				 enum mkga_operation operation,
				 const char *container_id)
{
	mkga_envelope_init(req);
	(void)snprintf(req->id, sizeof(req->id), "%s", request_id);
	req->kind = MKGA_MESSAGE_REQUEST;
	req->operation = operation;
	req->peer_kernel_id = 7;
	(void)snprintf(req->kernel_id, sizeof(req->kernel_id), "%s", "kernel-a");
	(void)snprintf(req->payload.container_control_req.kernel_id,
		       sizeof(req->payload.container_control_req.kernel_id),
		       "%s", "kernel-a");
	(void)snprintf(req->payload.container_control_req.container_id,
		       sizeof(req->payload.container_control_req.container_id),
		       "%s", container_id);
	req->payload.container_control_req.timeout_millis = 1000;
}

int main(void)
{
	struct mkga_transport *transport;
	struct mkga_runtime *runtime;
	struct mkga_agent agent;
	struct test_thread_ctx thread_ctx;
	volatile sig_atomic_t stop_flag = 0;
	pthread_t thread;
	struct mkga_envelope req;
	struct mkga_envelope resp;
	char container_id[MKGA_MAX_ID_LEN];

	transport = mkga_stub_transport_create(8);
	assert(transport != NULL);

	runtime = mkga_memory_runtime_create();
	assert(runtime != NULL);

	mkga_agent_init(&agent, transport, runtime, 50);
	thread_ctx.agent = &agent;
	thread_ctx.stop_flag = &stop_flag;
	thread_ctx.rc = -1;

	assert(pthread_create(&thread, NULL, run_agent, &thread_ctx) == 0);

	make_create_request(&req, "req-create");
	assert(mkga_stub_transport_push_request(transport, &req) == 0);
	assert(mkga_stub_transport_pop_response(transport, &resp, 1000) == 0);
	assert(resp.kind == MKGA_MESSAGE_RESPONSE);
	assert(!resp.error.present);
	assert(resp.payload.create_container_resp.container_id[0] != '\0');
	(void)snprintf(container_id, sizeof(container_id), "%s",
		       resp.payload.create_container_resp.container_id);

	make_control_request(&req, "req-start", MKGA_OP_START_CONTAINER, container_id);
	assert(mkga_stub_transport_push_request(transport, &req) == 0);
	assert(mkga_stub_transport_pop_response(transport, &resp, 1000) == 0);
	assert(resp.kind == MKGA_MESSAGE_RESPONSE);
	assert(!resp.error.present);

	make_control_request(&req, "req-stop", MKGA_OP_STOP_CONTAINER, container_id);
	assert(mkga_stub_transport_push_request(transport, &req) == 0);
	assert(mkga_stub_transport_pop_response(transport, &resp, 1000) == 0);
	assert(resp.kind == MKGA_MESSAGE_RESPONSE);
	assert(!resp.error.present);
	assert(resp.payload.stop_container_resp.exit_code == 0);

	make_control_request(&req, "req-remove", MKGA_OP_REMOVE_CONTAINER, container_id);
	assert(mkga_stub_transport_push_request(transport, &req) == 0);
	assert(mkga_stub_transport_pop_response(transport, &resp, 1000) == 0);
	assert(resp.kind == MKGA_MESSAGE_RESPONSE);
	assert(!resp.error.present);

	stop_flag = 1;
	mkga_stub_transport_shutdown(transport);
	assert(pthread_join(thread, NULL) == 0);
	assert(thread_ctx.rc == 0);

	mkga_runtime_destroy(runtime);
	mkga_transport_destroy(transport);
	(void)fprintf(stdout, "mk-guest-agent tests passed\n");
	return 0;
}
