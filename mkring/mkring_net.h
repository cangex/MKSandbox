#ifndef _MKRING_NET_H
#define _MKRING_NET_H

#ifdef __KERNEL__
#include <linux/types.h>
#else
#include <stdint.h>

typedef uint8_t __u8;
typedef uint16_t __u16;
typedef uint32_t __u32;
typedef uint64_t __u64;
#endif

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
	__u32 magic;
	__u8 version;
	__u8 channel;
	__u8 msg_type;
	__u8 flags;
	__u64 conn_id;
	__u32 seq;
	__u32 payload_len;
	__u32 src_ip_be;
	__u32 dst_ip_be;
	__u16 src_port_be;
	__u16 dst_port_be;
} __attribute__((packed));

struct mkring_net_message {
	struct mkring_net_header hdr;
	__u8 payload[MKRING_NET_MAX_PAYLOAD];
} __attribute__((packed));

#endif /* _MKRING_NET_H */
