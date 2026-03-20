#ifndef MKGA_CONFIG_H
#define MKGA_CONFIG_H

#include <stddef.h>
#include <stdint.h>

#define MKGA_DRIVER_NAME_LEN 32
#define MKGA_SOCKET_PATH_LEN 256

struct mkga_config {
	char transport_driver[MKGA_DRIVER_NAME_LEN];
	char runtime_driver[MKGA_DRIVER_NAME_LEN];
	char containerd_socket[MKGA_SOCKET_PATH_LEN];
	char bridge_device[MKGA_SOCKET_PATH_LEN];
	uint16_t peer_kernel_id;
	size_t inbound_buffer;
	int receive_timeout_ms;
};

void mkga_config_load_from_env(struct mkga_config *config);

#endif
