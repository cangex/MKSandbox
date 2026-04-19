#ifndef MKGA_MKRING_NET_H
#define MKGA_MKRING_NET_H

#include <stdint.h>

#include "mkring_proto.h"

#define MKRING_NET_CHANNEL        MKRING_CHANNEL_NET
#define MKRING_NET_MAX_PAYLOAD    768U

enum mkring_net_type {
	MKRING_NET_OPEN = 1,
	MKRING_NET_OPEN_OK = 2,
	MKRING_NET_OPEN_ERR = 3,
	MKRING_NET_DATA = 4,
	MKRING_NET_CLOSE = 5,
	MKRING_NET_RESET = 6,
	MKRING_NET_CREDIT = 7,
};

struct mkring_net_header {
	uint32_t magic;
	uint8_t version;
	uint8_t channel;
	uint8_t msg_type;
	uint8_t flags;
	uint64_t conn_id;
	uint32_t seq;
	uint32_t payload_len;
	uint32_t src_ip_be;
	uint32_t dst_ip_be;
	uint16_t src_port_be;
	uint16_t dst_port_be;
} __attribute__((packed));

struct mkring_net_message {
	struct mkring_net_header hdr;
	uint8_t payload[MKRING_NET_MAX_PAYLOAD];
} __attribute__((packed));

#endif
