#include "mkga_runtime.h"

#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define MKGA_MEMORY_RUNTIME_MAX_CONTAINERS 64

struct mkga_memory_container {
	bool in_use;
	bool running;
	char id[MKGA_MAX_ID_LEN];
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char image[MKGA_MAX_IMAGE_LEN];
	int32_t exit_code;
};

struct mkga_memory_runtime {
	pthread_mutex_t mu;
	unsigned int next_id;
	struct mkga_memory_container containers[MKGA_MEMORY_RUNTIME_MAX_CONTAINERS];
};

static struct mkga_memory_container *mkga_memory_find_container(
	struct mkga_memory_runtime *impl, const char *container_id)
{
	size_t i;

	for (i = 0; i < MKGA_MEMORY_RUNTIME_MAX_CONTAINERS; i++) {
		if (impl->containers[i].in_use &&
		    strcmp(impl->containers[i].id, container_id) == 0) {
			return &impl->containers[i];
		}
	}
	return NULL;
}

static struct mkga_memory_container *mkga_memory_alloc_container(
	struct mkga_memory_runtime *impl)
{
	size_t i;

	for (i = 0; i < MKGA_MEMORY_RUNTIME_MAX_CONTAINERS; i++) {
		if (!impl->containers[i].in_use) {
			return &impl->containers[i];
		}
	}
	return NULL;
}

static int mkga_memory_create_container(struct mkga_runtime *runtime,
					const struct mkga_create_container_req *req,
					struct mkga_create_container_resp *resp)
{
	struct mkga_memory_runtime *impl = runtime->impl;
	struct mkga_memory_container *container;

	pthread_mutex_lock(&impl->mu);
	container = mkga_memory_alloc_container(impl);
	if (!container) {
		pthread_mutex_unlock(&impl->mu);
		return -ENOSPC;
	}

	memset(container, 0, sizeof(*container));
	container->in_use = true;
	impl->next_id++;
	(void)snprintf(container->id, sizeof(container->id), "ctr-%u", impl->next_id);
	(void)snprintf(container->kernel_id, sizeof(container->kernel_id), "%s", req->kernel_id);
	(void)snprintf(container->image, sizeof(container->image), "%s", req->image);

	memset(resp, 0, sizeof(*resp));
	(void)snprintf(resp->container_id, sizeof(resp->container_id), "%s", container->id);
	(void)snprintf(resp->image_ref, sizeof(resp->image_ref), "%s", req->image);
	pthread_mutex_unlock(&impl->mu);
	return 0;
}

static int mkga_memory_start_container(struct mkga_runtime *runtime,
				       const struct mkga_container_control_req *req)
{
	struct mkga_memory_runtime *impl = runtime->impl;
	struct mkga_memory_container *container;

	pthread_mutex_lock(&impl->mu);
	container = mkga_memory_find_container(impl, req->container_id);
	if (!container) {
		pthread_mutex_unlock(&impl->mu);
		return -ENOENT;
	}
	container->running = true;
	container->exit_code = 0;
	pthread_mutex_unlock(&impl->mu);
	return 0;
}

static int mkga_memory_stop_container(struct mkga_runtime *runtime,
				      const struct mkga_container_control_req *req,
				      struct mkga_stop_container_resp *resp)
{
	struct mkga_memory_runtime *impl = runtime->impl;
	struct mkga_memory_container *container;

	pthread_mutex_lock(&impl->mu);
	container = mkga_memory_find_container(impl, req->container_id);
	if (!container) {
		pthread_mutex_unlock(&impl->mu);
		return -ENOENT;
	}
	container->running = false;
	container->exit_code = 0;
	resp->exit_code = 0;
	pthread_mutex_unlock(&impl->mu);
	return 0;
}

static int mkga_memory_status_container(struct mkga_runtime *runtime,
					const struct mkga_container_control_req *req,
					struct mkga_container_status_resp *resp)
{
	struct mkga_memory_runtime *impl = runtime->impl;
	struct mkga_memory_container *container;

	pthread_mutex_lock(&impl->mu);
	container = mkga_memory_find_container(impl, req->container_id);
	if (!container) {
		pthread_mutex_unlock(&impl->mu);
		return -ENOENT;
	}

	memset(resp, 0, sizeof(*resp));
	resp->state = container->running ? MKGA_CONTAINER_STATE_RUNNING :
		       MKGA_CONTAINER_STATE_CREATED;
	resp->exit_code = container->exit_code;
	pthread_mutex_unlock(&impl->mu);
	return 0;
}

static int mkga_memory_read_log(struct mkga_runtime *runtime,
				const struct mkga_read_log_req *req,
				struct mkga_read_log_resp *resp)
{
	struct mkga_memory_runtime *impl = runtime->impl;
	struct mkga_memory_container *container;

	pthread_mutex_lock(&impl->mu);
	container = mkga_memory_find_container(impl, req->container_id);
	if (!container) {
		pthread_mutex_unlock(&impl->mu);
		return -ENOENT;
	}

	memset(resp, 0, sizeof(*resp));
	resp->next_offset = req->offset;
	resp->eof = !container->running;
	pthread_mutex_unlock(&impl->mu);
	return 0;
}

static int mkga_memory_remove_container(struct mkga_runtime *runtime,
					const struct mkga_container_control_req *req)
{
	struct mkga_memory_runtime *impl = runtime->impl;
	struct mkga_memory_container *container;

	pthread_mutex_lock(&impl->mu);
	container = mkga_memory_find_container(impl, req->container_id);
	if (!container) {
		pthread_mutex_unlock(&impl->mu);
		return -ENOENT;
	}
	memset(container, 0, sizeof(*container));
	pthread_mutex_unlock(&impl->mu);
	return 0;
}

static void mkga_memory_destroy(struct mkga_runtime *runtime)
{
	struct mkga_memory_runtime *impl;

	if (!runtime) {
		return;
	}
	impl = runtime->impl;
	if (impl) {
		pthread_mutex_destroy(&impl->mu);
		free(impl);
	}
	free(runtime);
}

static const struct mkga_runtime_ops mkga_memory_ops = {
	.create_container = mkga_memory_create_container,
	.start_container = mkga_memory_start_container,
	.stop_container = mkga_memory_stop_container,
	.remove_container = mkga_memory_remove_container,
	.status_container = mkga_memory_status_container,
	.read_log = mkga_memory_read_log,
	.destroy = mkga_memory_destroy,
};

int mkga_runtime_create_container(struct mkga_runtime *runtime,
				  const struct mkga_create_container_req *req,
				  struct mkga_create_container_resp *resp)
{
	if (!runtime || !runtime->ops || !runtime->ops->create_container || !req ||
	    !resp) {
		return -EINVAL;
	}
	if (!runtime->impl) {
		return -EINVAL;
	}
	return runtime->ops->create_container(runtime, req, resp);
}

int mkga_runtime_start_container(struct mkga_runtime *runtime,
				 const struct mkga_container_control_req *req)
{
	if (!runtime || !runtime->ops || !runtime->ops->start_container || !req) {
		return -EINVAL;
	}
	if (!runtime->impl) {
		return -EINVAL;
	}
	return runtime->ops->start_container(runtime, req);
}

int mkga_runtime_stop_container(struct mkga_runtime *runtime,
				const struct mkga_container_control_req *req,
				struct mkga_stop_container_resp *resp)
{
	if (!runtime || !runtime->ops || !runtime->ops->stop_container || !req ||
	    !resp) {
		return -EINVAL;
	}
	if (!runtime->impl) {
		return -EINVAL;
	}
	return runtime->ops->stop_container(runtime, req, resp);
}

int mkga_runtime_remove_container(struct mkga_runtime *runtime,
				  const struct mkga_container_control_req *req)
{
	if (!runtime || !runtime->ops || !runtime->ops->remove_container || !req) {
		return -EINVAL;
	}
	if (!runtime->impl) {
		return -EINVAL;
	}
	return runtime->ops->remove_container(runtime, req);
}

int mkga_runtime_status_container(struct mkga_runtime *runtime,
				  const struct mkga_container_control_req *req,
				  struct mkga_container_status_resp *resp)
{
	if (!runtime || !runtime->ops || !runtime->ops->status_container || !req ||
	    !resp) {
		return -EINVAL;
	}
	if (!runtime->impl) {
		return -EINVAL;
	}
	return runtime->ops->status_container(runtime, req, resp);
}

int mkga_runtime_read_log(struct mkga_runtime *runtime,
			  const struct mkga_read_log_req *req,
			  struct mkga_read_log_resp *resp)
{
	if (!runtime || !runtime->ops || !runtime->ops->read_log || !req || !resp) {
		return -EINVAL;
	}
	if (!runtime->impl) {
		return -EINVAL;
	}
	return runtime->ops->read_log(runtime, req, resp);
}

void mkga_runtime_destroy(struct mkga_runtime *runtime)
{
	if (runtime && runtime->ops && runtime->ops->destroy) {
		runtime->ops->destroy(runtime);
	}
}

struct mkga_runtime *mkga_memory_runtime_create(void)
{
	struct mkga_runtime *runtime;
	struct mkga_memory_runtime *impl;

	runtime = calloc(1, sizeof(*runtime));
	impl = calloc(1, sizeof(*impl));
	if (!runtime || !impl) {
		free(runtime);
		free(impl);
		return NULL;
	}

	if (pthread_mutex_init(&impl->mu, NULL) != 0) {
		free(runtime);
		free(impl);
		return NULL;
	}

	impl->next_id = 0;
	runtime->ops = &mkga_memory_ops;
	runtime->impl = impl;
	return runtime;
}
