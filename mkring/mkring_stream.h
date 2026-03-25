#ifndef _MKRING_STREAM_H
#define _MKRING_STREAM_H

#ifdef __KERNEL__
#include <linux/types.h>
#else
#include <stdint.h>

typedef uint8_t __u8;
typedef int32_t __s32;
typedef uint32_t __u32;
typedef uint64_t __u64;
#endif

#include "mkring_proto.h"

#define MKRING_STREAM_MAGIC      MKRING_PROTO_MAGIC
#define MKRING_STREAM_VERSION    MKRING_PROTO_VERSION
#define MKRING_STREAM_CHANNEL    MKRING_CHANNEL_STREAM

#define MKRING_STREAM_TYPE_STDIN   1U
#define MKRING_STREAM_TYPE_OUTPUT  2U
#define MKRING_STREAM_TYPE_CONTROL 3U

#define MKRING_STREAM_CTL_RESIZE 1U
#define MKRING_STREAM_CTL_CLOSE  2U
#define MKRING_STREAM_CTL_EXIT   3U
#define MKRING_STREAM_CTL_ERROR  4U

#define MKRING_STREAM_FLAG_FINAL (1U << 0)

#define MKRING_STREAM_MAX_SESSION_ID 64U
#define MKRING_STREAM_MAX_PAYLOAD    768U

struct mkring_stream_header {
	__u32 magic;
	__u8 version;
	__u8 channel;
	__u8 stream_type;
	__u8 flags;
	__u64 session_seq;
	char session_id[MKRING_STREAM_MAX_SESSION_ID];
	__u32 payload_len;
} __attribute__((packed));

struct mkring_stream_control_resize {
	__u32 kind;
	__u32 width;
	__u32 height;
} __attribute__((packed));

struct mkring_stream_control_exit {
	__u32 kind;
	__s32 exit_code;
} __attribute__((packed));

struct mkring_stream_message {
	struct mkring_stream_header hdr;
	__u8 payload[MKRING_STREAM_MAX_PAYLOAD];
} __attribute__((packed));

struct mkring_stream_packet {
	__u16 peer_kernel_id;
	__u16 reserved0;
	struct mkring_stream_message msg;
} __attribute__((packed));

#endif /* _MKRING_STREAM_H */
