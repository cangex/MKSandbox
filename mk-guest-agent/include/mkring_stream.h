#ifndef _MKRING_STREAM_H
#define _MKRING_STREAM_H

#include <stdint.h>

#define MKRING_STREAM_MAGIC   0x4d4b434eU
#define MKRING_STREAM_VERSION 1
#define MKRING_STREAM_CHANNEL 3

#define MKRING_STREAM_TYPE_STDIN   1U
#define MKRING_STREAM_TYPE_OUTPUT  2U
#define MKRING_STREAM_TYPE_CONTROL 3U

#define MKRING_STREAM_CTL_RESIZE 1U
#define MKRING_STREAM_CTL_CLOSE  2U
#define MKRING_STREAM_CTL_EXIT   3U
#define MKRING_STREAM_CTL_ERROR  4U

#define MKRING_STREAM_FLAG_FINAL  (1U << 0)

#define MKRING_STREAM_MAX_SESSION_ID 64U
#define MKRING_STREAM_MAX_PAYLOAD    768U

struct mkring_stream_header {
	uint32_t magic;
	uint8_t version;
	uint8_t channel;
	uint8_t stream_type;
	uint8_t flags;
	uint64_t session_seq;
	char session_id[MKRING_STREAM_MAX_SESSION_ID];
	uint32_t payload_len;
} __attribute__((packed));

struct mkring_stream_control_resize {
	uint32_t kind;
	uint32_t width;
	uint32_t height;
} __attribute__((packed));

struct mkring_stream_control_exit {
	uint32_t kind;
	int32_t exit_code;
} __attribute__((packed));

struct mkring_stream_message {
	struct mkring_stream_header hdr;
	uint8_t payload[MKRING_STREAM_MAX_PAYLOAD];
} __attribute__((packed));

struct mkring_stream_packet {
	uint16_t peer_kernel_id;
	uint16_t reserved0;
	struct mkring_stream_message msg;
} __attribute__((packed));

#endif /* _MKRING_STREAM_H */
