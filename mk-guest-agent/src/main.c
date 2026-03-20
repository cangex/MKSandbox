#include "mkga_agent.h"
#include "mkga_config.h"
#include "mkring_container.h"

#include <stdbool.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static volatile sig_atomic_t mkga_stop = 0;

static void mkga_handle_signal(int signo)
{
	(void)signo;
	mkga_stop = 1;
}

static int mkga_install_signal_handlers(void)
{
	struct sigaction sa;

	memset(&sa, 0, sizeof(sa));
	sa.sa_handler = mkga_handle_signal;
	if (sigaction(SIGINT, &sa, NULL) != 0) {
		return -1;
	}
	if (sigaction(SIGTERM, &sa, NULL) != 0) {
		return -1;
	}
	return 0;
}

int main(void)
{
	struct mkga_config config;
	struct mkga_transport *transport = NULL;
	struct mkga_runtime *runtime = NULL;
	struct mkga_agent agent;
	bool use_stub_transport = false;
	uint32_t ready_features = 0;
	int rc;

	mkga_config_load_from_env(&config);
	if (mkga_install_signal_handlers() != 0) {
		perror("sigaction");
		return 1;
	}

	if (strcmp(config.runtime_driver, "memory") == 0) {
		runtime = mkga_memory_runtime_create();
	} else if (strcmp(config.runtime_driver, "containerd") == 0) {
		runtime = mkga_containerd_runtime_create(config.containerd_socket);
		ready_features = MKRING_CONTAINER_FEATURE_CONTAINERD;
	} else {
		(void)fprintf(stderr, "unsupported runtime driver: %s\n",
			      config.runtime_driver);
		return 1;
	}

	if (!runtime) {
		(void)fprintf(stderr, "failed to create runtime\n");
		return 1;
	}

	if (strcmp(config.transport_driver, "stub") == 0) {
		transport = mkga_stub_transport_create(config.inbound_buffer);
		use_stub_transport = true;
	} else if (strcmp(config.transport_driver, "mkring-device") == 0) {
		transport = mkga_mkring_device_transport_create(
			config.bridge_device,
			config.peer_kernel_id,
			config.runtime_driver,
			ready_features);
	} else {
		(void)fprintf(stderr, "unsupported transport driver: %s\n",
			      config.transport_driver);
		mkga_runtime_destroy(runtime);
		return 1;
	}

	if (!transport) {
		perror("mkga transport create");
		mkga_runtime_destroy(runtime);
		return 1;
	}

	mkga_agent_init(&agent, transport, runtime, config.receive_timeout_ms);
	(void)fprintf(stdout,
		      "mk-guest-agent started transport=%s runtime=%s\n",
		      config.transport_driver,
		      config.runtime_driver);

	rc = mkga_agent_serve(&agent, &mkga_stop);
	if (rc != 0) {
		(void)fprintf(stderr, "mk-guest-agent stopped with error=%d\n", rc);
	}

	if (use_stub_transport) {
		mkga_stub_transport_shutdown(transport);
	}
	mkga_runtime_destroy(runtime);
	mkga_transport_destroy(transport);
	return rc == 0 ? 0 : 1;
}
