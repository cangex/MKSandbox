#include "mkga_config.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

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

static int mkga_getenv_int(const char *key, int fallback)
{
	const char *value = getenv(key);
	char *end = NULL;
	long parsed;

	if (!value || value[0] == '\0') {
		return fallback;
	}

	parsed = strtol(value, &end, 10);
	if (!end || *end != '\0' || parsed <= 0) {
		return fallback;
	}
	return (int)parsed;
}

static uint16_t mkga_getenv_u16(const char *key, uint16_t fallback)
{
	const char *value = getenv(key);
	char *end = NULL;
	unsigned long parsed;

	if (!value || value[0] == '\0') {
		return fallback;
	}

	parsed = strtoul(value, &end, 10);
	if (!end || *end != '\0' || parsed > 65535UL) {
		return fallback;
	}
	return (uint16_t)parsed;
}

void mkga_config_load_from_env(struct mkga_config *config)
{
	const char *transport_driver = getenv("MK_GUEST_AGENT_TRANSPORT");
	const char *runtime_driver = getenv("MK_GUEST_AGENT_RUNTIME");
	const char *containerd_socket = getenv("MK_GUEST_AGENT_CONTAINERD_SOCKET");
	const char *bridge_device = getenv("MK_GUEST_AGENT_BRIDGE_DEVICE");

	if (!config) {
		return;
	}

	mkga_copy_string(config->transport_driver,
			 sizeof(config->transport_driver),
			 transport_driver ? transport_driver : "stub");
	mkga_copy_string(config->runtime_driver,
			 sizeof(config->runtime_driver),
			 runtime_driver ? runtime_driver : "memory");
	mkga_copy_string(config->containerd_socket,
			 sizeof(config->containerd_socket),
			 containerd_socket ? containerd_socket
					  : "/run/containerd/containerd.sock");
	mkga_copy_string(config->bridge_device,
			 sizeof(config->bridge_device),
			 bridge_device ? bridge_device : "/dev/mkring_container_bridge");
	config->peer_kernel_id =
		mkga_getenv_u16("MK_GUEST_AGENT_PEER_KERNEL_ID", 0);
	config->inbound_buffer = (size_t)mkga_getenv_int(
		"MK_GUEST_AGENT_INBOUND_BUFFER", 32);
	config->receive_timeout_ms = mkga_getenv_int(
		"MK_GUEST_AGENT_RECEIVE_TIMEOUT_MS", 200);
}
