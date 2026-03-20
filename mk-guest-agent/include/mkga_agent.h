#ifndef MKGA_AGENT_H
#define MKGA_AGENT_H

#include <signal.h>

#include "mkga_runtime.h"
#include "mkga_transport.h"

struct mkga_agent {
	struct mkga_transport *transport;
	struct mkga_runtime *runtime;
	int receive_timeout_ms;
};

void mkga_agent_init(struct mkga_agent *agent,
		     struct mkga_transport *transport,
		     struct mkga_runtime *runtime,
		     int receive_timeout_ms);
int mkga_agent_handle(struct mkga_agent *agent,
		      const struct mkga_envelope *req,
		      struct mkga_envelope *resp);
int mkga_agent_serve(struct mkga_agent *agent, volatile sig_atomic_t *stop_flag);

#endif
