#ifndef _MKRING_CONTAINER_H
#define _MKRING_CONTAINER_H

#ifdef __KERNEL__
#include <linux/ioctl.h>
#include <linux/types.h>
#else
#include <stdint.h>
#include <sys/ioctl.h>

typedef uint8_t __u8;
typedef int8_t __s8;
typedef uint16_t __u16;
typedef int16_t __s16;
typedef uint32_t __u32;
typedef int32_t __s32;
typedef uint64_t __u64;
typedef int64_t __s64;
#endif

#include "mkring_proto.h"

#define MKRING_CONTAINER_MAGIC			MKRING_PROTO_MAGIC
#define MKRING_CONTAINER_VERSION		MKRING_PROTO_VERSION
#define MKRING_CONTAINER_CHANNEL		MKRING_CHANNEL_CONTAINER

#define MKRING_CONTAINER_KIND_READY		MKRING_PROTO_KIND_READY
#define MKRING_CONTAINER_KIND_REQUEST		MKRING_PROTO_KIND_REQUEST
#define MKRING_CONTAINER_KIND_RESPONSE		MKRING_PROTO_KIND_RESPONSE

#define MKRING_CONTAINER_OP_NONE		0U
#define MKRING_CONTAINER_OP_CREATE		1U
#define MKRING_CONTAINER_OP_START		2U
#define MKRING_CONTAINER_OP_STOP		3U
#define MKRING_CONTAINER_OP_REMOVE		4U
#define MKRING_CONTAINER_OP_STATUS		5U
#define MKRING_CONTAINER_OP_READ_LOG		6U
#define MKRING_CONTAINER_OP_EXEC_TTY_PREPARE	7U
#define MKRING_CONTAINER_OP_EXEC_TTY_START	8U
#define MKRING_CONTAINER_OP_EXEC_TTY_RESIZE	9U
#define MKRING_CONTAINER_OP_EXEC_TTY_CLOSE	10U

#define MKRING_CONTAINER_STATE_UNKNOWN		0U
#define MKRING_CONTAINER_STATE_CREATED		1U
#define MKRING_CONTAINER_STATE_RUNNING		2U
#define MKRING_CONTAINER_STATE_EXITED		3U

#define MKRING_CONTAINER_FEATURE_CONTAINERD	(1U << 0)

#define MKRING_CONTAINER_MAX_RUNTIME_NAME	16U
#define MKRING_CONTAINER_MAX_KERNEL_ID		64U
#define MKRING_CONTAINER_MAX_POD_ID		64U
#define MKRING_CONTAINER_MAX_NAME		64U
#define MKRING_CONTAINER_MAX_IMAGE		256U
#define MKRING_CONTAINER_MAX_LOG_PATH		256U
#define MKRING_CONTAINER_MAX_ARGV		4U
#define MKRING_CONTAINER_MAX_ARG_LEN		64U
#define MKRING_CONTAINER_MAX_CONTAINER_ID	64U
#define MKRING_CONTAINER_MAX_IMAGE_REF		256U
#define MKRING_CONTAINER_MAX_ERROR_MSG		128U
#define MKRING_CONTAINER_MAX_LOG_CHUNK		384U

struct mkring_container_ready_payload {
	__u32 features;
	char runtime_name[MKRING_CONTAINER_MAX_RUNTIME_NAME];
} __attribute__((packed));

struct mkring_container_create_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char pod_id[MKRING_CONTAINER_MAX_POD_ID];
	char name[MKRING_CONTAINER_MAX_NAME];
	char image[MKRING_CONTAINER_MAX_IMAGE];
	char log_path[MKRING_CONTAINER_MAX_LOG_PATH];
	__u32 argv_count;
	__u32 reserved0;
	char argv[MKRING_CONTAINER_MAX_ARGV][MKRING_CONTAINER_MAX_ARG_LEN];
} __attribute__((packed));

struct mkring_container_control_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	__s64 timeout_millis;
} __attribute__((packed));

struct mkring_container_create_response {
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	char image_ref[MKRING_CONTAINER_MAX_IMAGE_REF];
} __attribute__((packed));

struct mkring_container_stop_response {
	__s32 exit_code;
} __attribute__((packed));

struct mkring_container_status_response {
	__u32 state;
	__s32 exit_code;
	__s32 pid;
	__u64 started_at_unix_nano;
	__u64 finished_at_unix_nano;
	char message[MKRING_CONTAINER_MAX_ERROR_MSG];
} __attribute__((packed));

struct mkring_container_read_log_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	__u64 offset;
	__u32 max_bytes;
} __attribute__((packed));

struct mkring_container_read_log_response {
	__u64 next_offset;
	__u32 data_len;
	__u8 eof;
	__u8 reserved0[3];
	char data[MKRING_CONTAINER_MAX_LOG_CHUNK];
} __attribute__((packed));

struct mkring_container_exec_tty_prepare_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	__u32 argv_count;
	__u8 tty;
	__u8 stdin_enabled;
	__u8 stdout_enabled;
	__u8 stderr_enabled;
	char argv[MKRING_CONTAINER_MAX_ARGV][MKRING_CONTAINER_MAX_ARG_LEN];
} __attribute__((packed));

struct mkring_container_exec_tty_prepare_response {
	char session_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
} __attribute__((packed));

struct mkring_container_exec_tty_start_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char session_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
} __attribute__((packed));

struct mkring_container_exec_tty_resize_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char session_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	__u32 width;
	__u32 height;
} __attribute__((packed));

struct mkring_container_exec_tty_close_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char session_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
} __attribute__((packed));

struct mkring_container_error_payload {
	__s32 errno_value;
	char message[MKRING_CONTAINER_MAX_ERROR_MSG];
} __attribute__((packed));

union mkring_container_payload {
	struct mkring_container_ready_payload ready;
	struct mkring_container_create_request create_req;
	struct mkring_container_control_request control_req;
	struct mkring_container_create_response create_resp;
	struct mkring_container_stop_response stop_resp;
	struct mkring_container_status_response status_resp;
	struct mkring_container_read_log_request read_log_req;
	struct mkring_container_read_log_response read_log_resp;
	struct mkring_container_exec_tty_prepare_request exec_tty_prepare_req;
	struct mkring_container_exec_tty_prepare_response exec_tty_prepare_resp;
	struct mkring_container_exec_tty_start_request exec_tty_start_req;
	struct mkring_container_exec_tty_resize_request exec_tty_resize_req;
	struct mkring_container_exec_tty_close_request exec_tty_close_req;
	struct mkring_container_error_payload error;
} __attribute__((packed));

struct mkring_container_message {
	struct mkring_proto_header hdr;
	union mkring_container_payload payload;
} __attribute__((packed));

struct mkring_container_packet {
	__u16 peer_kernel_id;
	__u16 reserved0;
	struct mkring_container_message msg;
} __attribute__((packed));

struct mkring_container_wait_ready {
	__u16 peer_kernel_id;
	__u16 reserved0;
	__u32 timeout_ms;
	__u32 ready;
} __attribute__((packed));

struct mkring_container_set_ready {
	__u16 peer_kernel_id;
	__u16 reserved0;
	__u32 features;
	char runtime_name[MKRING_CONTAINER_MAX_RUNTIME_NAME];
} __attribute__((packed));

struct mkring_container_call {
	__u16 peer_kernel_id;
	__u16 reserved0;
	__u32 timeout_ms;
	__s32 status;
	struct mkring_container_message request;
	struct mkring_container_message response;
} __attribute__((packed));

#define MKRING_CONTAINER_IOC_MAGIC		0xB7
#define MKRING_CONTAINER_IOC_WAIT_READY \
	_IOWR(MKRING_CONTAINER_IOC_MAGIC, 0x01, struct mkring_container_wait_ready)
#define MKRING_CONTAINER_IOC_SET_READY \
	_IOW(MKRING_CONTAINER_IOC_MAGIC, 0x02, struct mkring_container_set_ready)
#define MKRING_CONTAINER_IOC_CALL \
	_IOWR(MKRING_CONTAINER_IOC_MAGIC, 0x03, struct mkring_container_call)

#endif /* _MKRING_CONTAINER_H */
