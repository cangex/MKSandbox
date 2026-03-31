#ifndef _MKRING_TRANSPORT_UAPI_H
#define _MKRING_TRANSPORT_UAPI_H

#include <stdint.h>

typedef uint8_t __u8;
typedef int8_t __s8;
typedef uint16_t __u16;
typedef int16_t __s16;
typedef uint32_t __u32;
typedef int32_t __s32;
typedef uint64_t __u64;
typedef int64_t __s64;

/*
 * Guest-side mirrored direct-entry transport UAPI.
 *
 * Keep this header in sync with ../mkring/mkring_transport_uapi.h.
 */

#define MKRING_TRANSPORT_UAPI_VERSION 1U
#define MKRING_TRANSPORT_MAX_MESSAGE  1024U

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
