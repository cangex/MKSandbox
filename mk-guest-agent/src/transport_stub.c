#include "mkga_transport.h"

#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdlib.h>
#include <string.h>
#include <sys/time.h>

struct mkga_ring_queue {
	struct mkga_envelope *items;
	size_t capacity;
	size_t head;
	size_t tail;
	size_t count;
};

struct mkga_stub_transport {
	pthread_mutex_t mu;
	pthread_cond_t cv;
	bool closed;
	struct mkga_ring_queue inbound;
	struct mkga_ring_queue outbound;
};

static int mkga_queue_init(struct mkga_ring_queue *queue, size_t capacity)
{
	queue->items = calloc(capacity, sizeof(*queue->items));
	if (!queue->items) {
		return -ENOMEM;
	}
	queue->capacity = capacity;
	queue->head = 0;
	queue->tail = 0;
	queue->count = 0;
	return 0;
}

static void mkga_queue_destroy(struct mkga_ring_queue *queue)
{
	free(queue->items);
	memset(queue, 0, sizeof(*queue));
}

static int mkga_queue_push(struct mkga_ring_queue *queue,
			   const struct mkga_envelope *item)
{
	if (queue->count == queue->capacity) {
		return -ENOSPC;
	}

	queue->items[queue->tail] = *item;
	queue->tail = (queue->tail + 1U) % queue->capacity;
	queue->count++;
	return 0;
}

static int mkga_queue_pop(struct mkga_ring_queue *queue,
			  struct mkga_envelope *item)
{
	if (queue->count == 0) {
		return -ENOENT;
	}

	*item = queue->items[queue->head];
	queue->head = (queue->head + 1U) % queue->capacity;
	queue->count--;
	return 0;
}

static int mkga_wait_for_item(struct mkga_stub_transport *stub,
			      struct mkga_ring_queue *queue,
			      int timeout_ms)
{
	int rc = 0;

	while (!stub->closed && queue->count == 0) {
		if (timeout_ms < 0) {
			rc = pthread_cond_wait(&stub->cv, &stub->mu);
		} else {
			struct timespec ts;
			struct timeval tv;

			gettimeofday(&tv, NULL);
			ts.tv_sec = tv.tv_sec;
			ts.tv_nsec = (long)tv.tv_usec * 1000L;
			ts.tv_sec += timeout_ms / 1000;
			ts.tv_nsec += (long)(timeout_ms % 1000) * 1000000L;
			if (ts.tv_nsec >= 1000000000L) {
				ts.tv_sec += 1;
				ts.tv_nsec -= 1000000000L;
			}
			rc = pthread_cond_timedwait(&stub->cv, &stub->mu, &ts);
		}

		if (rc == ETIMEDOUT) {
			return -ETIMEDOUT;
		}
		if (rc != 0) {
			return -rc;
		}
	}

	if (stub->closed && queue->count == 0) {
		return -ESHUTDOWN;
	}

	return 0;
}

static int mkga_stub_receive(struct mkga_transport *transport,
			     struct mkga_envelope *req,
			     int timeout_ms)
{
	struct mkga_stub_transport *stub = transport->impl;
	int rc;

	pthread_mutex_lock(&stub->mu);
	rc = mkga_wait_for_item(stub, &stub->inbound, timeout_ms);
	if (rc == 0) {
		rc = mkga_queue_pop(&stub->inbound, req);
	}
	pthread_mutex_unlock(&stub->mu);
	return rc;
}

static int mkga_stub_send(struct mkga_transport *transport,
			  const struct mkga_envelope *resp)
{
	struct mkga_stub_transport *stub = transport->impl;
	int rc;

	pthread_mutex_lock(&stub->mu);
	if (stub->closed) {
		pthread_mutex_unlock(&stub->mu);
		return -ESHUTDOWN;
	}
	rc = mkga_queue_push(&stub->outbound, resp);
	pthread_cond_broadcast(&stub->cv);
	pthread_mutex_unlock(&stub->mu);
	return rc;
}

static void mkga_stub_destroy(struct mkga_transport *transport)
{
	struct mkga_stub_transport *stub;

	if (!transport) {
		return;
	}
	stub = transport->impl;
	if (stub) {
		pthread_mutex_destroy(&stub->mu);
		pthread_cond_destroy(&stub->cv);
		mkga_queue_destroy(&stub->inbound);
		mkga_queue_destroy(&stub->outbound);
		free(stub);
	}
	free(transport);
}

static const struct mkga_transport_ops mkga_stub_ops = {
	.receive = mkga_stub_receive,
	.send = mkga_stub_send,
	.destroy = mkga_stub_destroy,
};

int mkga_transport_receive(struct mkga_transport *transport,
			   struct mkga_envelope *req,
			   int timeout_ms)
{
	if (!transport || !transport->ops || !transport->ops->receive || !req) {
		return -EINVAL;
	}
	if (!transport->impl) {
		return -EINVAL;
	}
	return transport->ops->receive(transport, req, timeout_ms);
}

int mkga_transport_send(struct mkga_transport *transport,
			const struct mkga_envelope *resp)
{
	if (!transport || !transport->ops || !transport->ops->send || !resp) {
		return -EINVAL;
	}
	if (!transport->impl) {
		return -EINVAL;
	}
	return transport->ops->send(transport, resp);
}

void mkga_transport_destroy(struct mkga_transport *transport)
{
	if (transport && transport->ops && transport->ops->destroy) {
		transport->ops->destroy(transport);
	}
}

struct mkga_transport *mkga_stub_transport_create(size_t capacity)
{
	struct mkga_transport *transport;
	struct mkga_stub_transport *stub;
	bool mu_ready = false;
	bool cv_ready = false;
	bool inbound_ready = false;
	bool outbound_ready = false;

	if (capacity == 0) {
		errno = EINVAL;
		return NULL;
	}

	transport = calloc(1, sizeof(*transport));
	stub = calloc(1, sizeof(*stub));
	if (!transport || !stub) {
		free(transport);
		free(stub);
		errno = ENOMEM;
		return NULL;
	}

	if (pthread_mutex_init(&stub->mu, NULL) != 0) {
		goto fail;
	}
	mu_ready = true;
	if (pthread_cond_init(&stub->cv, NULL) != 0) {
		goto fail;
	}
	cv_ready = true;
	if (mkga_queue_init(&stub->inbound, capacity) != 0) {
		goto fail;
	}
	inbound_ready = true;
	if (mkga_queue_init(&stub->outbound, capacity) != 0) {
		goto fail;
	}
	outbound_ready = true;

	transport->ops = &mkga_stub_ops;
	transport->impl = stub;
	return transport;

fail:
	if (stub) {
		if (outbound_ready) {
			mkga_queue_destroy(&stub->outbound);
		}
		if (inbound_ready) {
			mkga_queue_destroy(&stub->inbound);
		}
		if (cv_ready) {
			pthread_cond_destroy(&stub->cv);
		}
		if (mu_ready) {
			pthread_mutex_destroy(&stub->mu);
		}
	}
	free(transport);
	free(stub);
	errno = ENOMEM;
	return NULL;
}

int mkga_stub_transport_push_request(struct mkga_transport *transport,
				     const struct mkga_envelope *req)
{
	struct mkga_stub_transport *stub;
	int rc;

	if (!transport || !transport->impl || !req) {
		return -EINVAL;
	}
	stub = transport->impl;

	pthread_mutex_lock(&stub->mu);
	if (stub->closed) {
		pthread_mutex_unlock(&stub->mu);
		return -ESHUTDOWN;
	}
	rc = mkga_queue_push(&stub->inbound, req);
	pthread_cond_broadcast(&stub->cv);
	pthread_mutex_unlock(&stub->mu);
	return rc;
}

int mkga_stub_transport_pop_response(struct mkga_transport *transport,
				     struct mkga_envelope *resp,
				     int timeout_ms)
{
	struct mkga_stub_transport *stub;
	int rc;

	if (!transport || !transport->impl || !resp) {
		return -EINVAL;
	}
	stub = transport->impl;

	pthread_mutex_lock(&stub->mu);
	rc = mkga_wait_for_item(stub, &stub->outbound, timeout_ms);
	if (rc == 0) {
		rc = mkga_queue_pop(&stub->outbound, resp);
	}
	pthread_mutex_unlock(&stub->mu);
	return rc;
}

void mkga_stub_transport_shutdown(struct mkga_transport *transport)
{
	struct mkga_stub_transport *stub;

	if (!transport || !transport->impl) {
		return;
	}
	stub = transport->impl;

	pthread_mutex_lock(&stub->mu);
	stub->closed = true;
	pthread_cond_broadcast(&stub->cv);
	pthread_mutex_unlock(&stub->mu);
}
