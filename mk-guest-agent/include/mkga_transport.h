#ifndef MKGA_TRANSPORT_H
#define MKGA_TRANSPORT_H

#include <stddef.h>
#include <stdint.h>

#include "mkga_protocol.h"

struct mkga_transport;

struct mkga_transport_ops {
	int (*receive)(struct mkga_transport *transport,
		       struct mkga_envelope *req,
		       int timeout_ms);
	int (*send)(struct mkga_transport *transport,
		    const struct mkga_envelope *resp);
	void (*destroy)(struct mkga_transport *transport);
};

struct mkga_transport {
	const struct mkga_transport_ops *ops;
	void *impl;
};

int mkga_transport_receive(struct mkga_transport *transport,
			   struct mkga_envelope *req,
			   int timeout_ms);
int mkga_transport_send(struct mkga_transport *transport,
			const struct mkga_envelope *resp);
void mkga_transport_destroy(struct mkga_transport *transport);

struct mkga_transport *mkga_stub_transport_create(size_t capacity);
int mkga_stub_transport_push_request(struct mkga_transport *transport,
				     const struct mkga_envelope *req);
int mkga_stub_transport_pop_response(struct mkga_transport *transport,
				     struct mkga_envelope *resp,
				     int timeout_ms);
void mkga_stub_transport_shutdown(struct mkga_transport *transport);
struct mkga_transport *mkga_mkring_transport_create(
	uint16_t peer_kernel_id,
	const char *runtime_name,
	uint32_t features);

#endif
