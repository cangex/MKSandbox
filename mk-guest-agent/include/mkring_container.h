#ifndef MKGA_MKRING_CONTAINER_H
#define MKGA_MKRING_CONTAINER_H

#include <stdint.h>
#include <sys/ioctl.h>

#define MKRING_CONTAINER_MAGIC                   0x4d4b434eU
#define MKRING_CONTAINER_VERSION                 1U
#define MKRING_CONTAINER_CHANNEL                 1U

#define MKRING_CONTAINER_KIND_READY              1U
#define MKRING_CONTAINER_KIND_REQUEST            2U
#define MKRING_CONTAINER_KIND_RESPONSE           3U

#define MKRING_CONTAINER_OP_NONE                 0U
#define MKRING_CONTAINER_OP_CREATE               1U
#define MKRING_CONTAINER_OP_START                2U
#define MKRING_CONTAINER_OP_STOP                 3U
#define MKRING_CONTAINER_OP_REMOVE               4U
#define MKRING_CONTAINER_OP_STATUS               5U
#define MKRING_CONTAINER_OP_READ_LOG             6U
#define MKRING_CONTAINER_OP_EXEC_TTY_PREPARE     7U
#define MKRING_CONTAINER_OP_EXEC_TTY_START       8U
#define MKRING_CONTAINER_OP_EXEC_TTY_RESIZE      9U
#define MKRING_CONTAINER_OP_EXEC_TTY_CLOSE       10U

#define MKRING_CONTAINER_STATE_UNKNOWN           0U
#define MKRING_CONTAINER_STATE_CREATED           1U
#define MKRING_CONTAINER_STATE_RUNNING           2U
#define MKRING_CONTAINER_STATE_EXITED            3U

#define MKRING_CONTAINER_FEATURE_CONTAINERD      (1U << 0)

#define MKRING_CONTAINER_MAX_RUNTIME_NAME        16U
#define MKRING_CONTAINER_MAX_KERNEL_ID           64U
#define MKRING_CONTAINER_MAX_POD_ID              64U
#define MKRING_CONTAINER_MAX_NAME                64U
#define MKRING_CONTAINER_MAX_IMAGE               256U
#define MKRING_CONTAINER_MAX_LOG_PATH            256U
#define MKRING_CONTAINER_MAX_ARGV                4U
#define MKRING_CONTAINER_MAX_ARG_LEN             64U
#define MKRING_CONTAINER_MAX_CONTAINER_ID        64U
#define MKRING_CONTAINER_MAX_IMAGE_REF           256U
#define MKRING_CONTAINER_MAX_ERROR_MSG           128U
#define MKRING_CONTAINER_MAX_LOG_CHUNK           384U

struct mkring_container_header {
	uint32_t magic;
	uint8_t version;
	uint8_t channel;
	uint8_t kind;
	uint8_t operation;
	uint16_t flags;
	uint16_t reserved0;
	uint64_t request_id;
	int32_t status;
	uint32_t payload_len;
} __attribute__((packed));

struct mkring_container_ready_payload {
	uint32_t features;
	char runtime_name[MKRING_CONTAINER_MAX_RUNTIME_NAME];
} __attribute__((packed));

struct mkring_container_create_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char pod_id[MKRING_CONTAINER_MAX_POD_ID];
	char name[MKRING_CONTAINER_MAX_NAME];
	char image[MKRING_CONTAINER_MAX_IMAGE];
	char log_path[MKRING_CONTAINER_MAX_LOG_PATH];
	uint32_t argv_count;
	uint32_t reserved0;
	char argv[MKRING_CONTAINER_MAX_ARGV][MKRING_CONTAINER_MAX_ARG_LEN];
} __attribute__((packed));

struct mkring_container_control_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	int64_t timeout_millis;
} __attribute__((packed));

struct mkring_container_create_response {
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	char image_ref[MKRING_CONTAINER_MAX_IMAGE_REF];
} __attribute__((packed));

struct mkring_container_stop_response {
	int32_t exit_code;
} __attribute__((packed));

struct mkring_container_status_response {
	uint32_t state;
	int32_t exit_code;
	int32_t pid;
	uint64_t started_at_unix_nano;
	uint64_t finished_at_unix_nano;
	char message[MKRING_CONTAINER_MAX_ERROR_MSG];
} __attribute__((packed));

struct mkring_container_read_log_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	uint64_t offset;
	uint32_t max_bytes;
} __attribute__((packed));

struct mkring_container_read_log_response {
	uint64_t next_offset;
	uint32_t data_len;
	uint8_t eof;
	uint8_t reserved0[3];
	char data[MKRING_CONTAINER_MAX_LOG_CHUNK];
} __attribute__((packed));

struct mkring_container_exec_tty_prepare_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char container_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
	uint32_t argv_count;
	uint8_t tty;
	uint8_t stdin_enabled;
	uint8_t stdout_enabled;
	uint8_t stderr_enabled;
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
	uint32_t width;
	uint32_t height;
} __attribute__((packed));

struct mkring_container_exec_tty_close_request {
	char kernel_id[MKRING_CONTAINER_MAX_KERNEL_ID];
	char session_id[MKRING_CONTAINER_MAX_CONTAINER_ID];
} __attribute__((packed));

struct mkring_container_error_payload {
	int32_t errno_value;
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
	struct mkring_container_header hdr;
	union mkring_container_payload payload;
} __attribute__((packed));

struct mkring_container_packet {
	uint16_t peer_kernel_id;
	uint16_t reserved0;
	struct mkring_container_message msg;
} __attribute__((packed));

struct mkring_container_set_ready {
	uint16_t peer_kernel_id;
	uint16_t reserved0;
	uint32_t features;
	char runtime_name[MKRING_CONTAINER_MAX_RUNTIME_NAME];
} __attribute__((packed));

#define MKRING_CONTAINER_IOC_MAGIC               0xB7
#define MKRING_CONTAINER_IOC_SET_READY \
	_IOW(MKRING_CONTAINER_IOC_MAGIC, 0x02, struct mkring_container_set_ready)

#endif
