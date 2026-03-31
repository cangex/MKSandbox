#ifndef _MKRING_PROTO_H
#define _MKRING_PROTO_H

#include <stdbool.h>
#include <stdint.h>

typedef uint8_t __u8;
typedef int8_t __s8;
typedef uint16_t __u16;
typedef int16_t __s16;
typedef uint32_t __u32;
typedef int32_t __s32;
typedef uint64_t __u64;
typedef int64_t __s64;

#define MKRING_PROTO_MAGIC        0x4d4b434eU
#define MKRING_PROTO_VERSION      1U

#define MKRING_CHANNEL_CONTAINER  1U
#define MKRING_CHANNEL_SYSTEM     2U
#define MKRING_CHANNEL_STREAM     3U

#define MKRING_PROTO_KIND_READY     1U
#define MKRING_PROTO_KIND_REQUEST   2U
#define MKRING_PROTO_KIND_RESPONSE  3U

struct mkring_proto_header {
	__u32 magic;
	__u8 version;
	__u8 channel;
	__u8 kind;
	__u8 operation;
	__u16 flags;
	__u16 reserved0;
	__u64 request_id;
	__s32 status;
	__u32 payload_len;
} __attribute__((packed));

static inline bool mkring_proto_header_valid(const void *data, __u32 len)
{
	const struct mkring_proto_header *hdr = data;
	__u32 body_len;

	if (!hdr || len < sizeof(*hdr))
		return false;
	if (hdr->magic != MKRING_PROTO_MAGIC)
		return false;
	if (hdr->version != MKRING_PROTO_VERSION)
		return false;
	if (hdr->channel == 0)
		return false;

	body_len = len - (__u32)sizeof(*hdr);
	if (hdr->payload_len > body_len)
		return false;

	return true;
}

#endif /* _MKRING_PROTO_H */
