#ifndef _MKRING_TRANSPORT_UAPI_H
#define _MKRING_TRANSPORT_UAPI_H

#ifdef __KERNEL__
#include <linux/types.h>
#else
#include <stdint.h>

typedef uint8_t __u8;
typedef int8_t __s8;
typedef uint16_t __u16;
typedef int16_t __s16;
typedef uint32_t __u32;
typedef int32_t __s32;
typedef uint64_t __u64;
typedef int64_t __s64;
#endif

/*
 * Direct transport UAPI used by sys_mkring_transport.
 *
 * The kernel only provides transport semantics:
 * - send one opaque message to a peer
 * - receive one opaque message from a channel queue
 *
 * Message validity beyond the basic transport header and the meaning of the
 * payload are handled in userspace. The current control plane keeps using
 * serialized mkring container messages on channel 1 and stream packets on
 * channel 3.
 */

#define MKRING_TRANSPORT_UAPI_VERSION          1U
#define MKRING_TRANSPORT_MAX_MESSAGE           1024U

/*
 * Direct-entry op codes for the generic mkring transport path.
 */
enum mkring_transport_op {
	MKRING_TRANSPORT_OP_SEND = 1,
	MKRING_TRANSPORT_OP_RECV = 2,
};

struct mkring_transport_send {
	__u16 peer_kernel_id;
	__u16 channel;
	__u32 message_len;
	__u8 message[MKRING_TRANSPORT_MAX_MESSAGE];
} __attribute__((packed));

struct mkring_transport_recv {
	__u16 peer_kernel_id;
	__u16 channel;
	__u32 timeout_ms;
	__u32 message_len;
	__u8 message[MKRING_TRANSPORT_MAX_MESSAGE];
} __attribute__((packed));

#endif /* _MKRING_TRANSPORT_UAPI_H */
