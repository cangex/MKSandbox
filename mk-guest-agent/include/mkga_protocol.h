#ifndef MKGA_PROTOCOL_H
#define MKGA_PROTOCOL_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define MKGA_VERSION 1
#define MKGA_MAX_ID_LEN 64
#define MKGA_MAX_CODE_LEN 64
#define MKGA_MAX_MESSAGE_LEN 256
#define MKGA_MAX_KERNEL_ID_LEN 64
#define MKGA_MAX_POD_ID_LEN 64
#define MKGA_MAX_NAME_LEN 64
#define MKGA_MAX_IMAGE_LEN 256
#define MKGA_MAX_LOG_PATH_LEN 256
#define MKGA_MAX_ARGV 4
#define MKGA_MAX_ARG_LEN 64
#define MKGA_MAX_SESSION_ID_LEN 64
#define MKGA_MAX_KV_PAIRS 8
#define MKGA_MAX_KV_KEY_LEN 64
#define MKGA_MAX_KV_VALUE_LEN 256
#define MKGA_MAX_IP_LEN 16
#define MKGA_MAX_CIDR_LEN 24
#define MKGA_MAX_MODE_LEN 16
#define MKGA_MAX_ENDPOINTS 16
#define MKGA_MAX_PORTS 32
#define MKGA_MAX_ENV_KEY_LEN 64
#define MKGA_MAX_ENV_VALUE_LEN 256

enum mkga_message_kind {
	MKGA_MESSAGE_INVALID = 0,
	MKGA_MESSAGE_REQUEST = 1,
	MKGA_MESSAGE_RESPONSE = 2,
};

enum mkga_operation {
	MKGA_OP_INVALID = 0,
	MKGA_OP_CREATE_CONTAINER = 1,
	MKGA_OP_START_CONTAINER = 2,
	MKGA_OP_STOP_CONTAINER = 3,
	MKGA_OP_REMOVE_CONTAINER = 4,
	MKGA_OP_STATUS_CONTAINER = 5,
	MKGA_OP_READ_LOG = 6,
	MKGA_OP_EXEC_TTY_PREPARE = 7,
	MKGA_OP_EXEC_TTY_START = 8,
	MKGA_OP_EXEC_TTY_RESIZE = 9,
	MKGA_OP_EXEC_TTY_CLOSE = 10,
	MKGA_OP_CONFIGURE_NETWORK = 11,
	MKGA_OP_CONFIGURE_ENV = 12,
};

enum mkga_container_state {
	MKGA_CONTAINER_STATE_UNKNOWN = 0,
	MKGA_CONTAINER_STATE_CREATED = 1,
	MKGA_CONTAINER_STATE_RUNNING = 2,
	MKGA_CONTAINER_STATE_EXITED = 3,
};

struct mkga_kv_pair {
	char key[MKGA_MAX_KV_KEY_LEN];
	char value[MKGA_MAX_KV_VALUE_LEN];
};

struct mkga_error {
	bool present;
	char code[MKGA_MAX_CODE_LEN];
	char message[MKGA_MAX_MESSAGE_LEN];
};

struct mkga_create_container_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char pod_id[MKGA_MAX_POD_ID_LEN];
	char name[MKGA_MAX_NAME_LEN];
	char image[MKGA_MAX_IMAGE_LEN];
	char log_path[MKGA_MAX_LOG_PATH_LEN];
	uint32_t argv_count;
	char argv[MKGA_MAX_ARGV][MKGA_MAX_ARG_LEN];
	size_t label_count;
	struct mkga_kv_pair labels[MKGA_MAX_KV_PAIRS];
	size_t annotation_count;
	struct mkga_kv_pair annotations[MKGA_MAX_KV_PAIRS];
};

struct mkga_create_container_resp {
	char container_id[MKGA_MAX_ID_LEN];
	char image_ref[MKGA_MAX_IMAGE_LEN];
};

struct mkga_container_control_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char container_id[MKGA_MAX_ID_LEN];
	int64_t timeout_millis;
};

struct mkga_stop_container_resp {
	int32_t exit_code;
};

struct mkga_container_status_resp {
	uint32_t state;
	int32_t exit_code;
	int32_t pid;
	uint64_t started_at_unix_nano;
	uint64_t finished_at_unix_nano;
	char message[MKGA_MAX_MESSAGE_LEN];
};

struct mkga_read_log_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char container_id[MKGA_MAX_ID_LEN];
	uint64_t offset;
	uint32_t max_bytes;
};

#define MKGA_MAX_LOG_CHUNK 384

struct mkga_read_log_resp {
	uint64_t next_offset;
	uint32_t data_len;
	bool eof;
	uint8_t data[MKGA_MAX_LOG_CHUNK];
};

struct mkga_exec_tty_prepare_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char container_id[MKGA_MAX_ID_LEN];
	uint32_t argv_count;
	uint8_t tty;
	uint8_t stdin_enabled;
	uint8_t stdout_enabled;
	uint8_t stderr_enabled;
	char argv[MKGA_MAX_ARGV][MKGA_MAX_ARG_LEN];
};

struct mkga_exec_tty_prepare_resp {
	char session_id[MKGA_MAX_SESSION_ID_LEN];
};

struct mkga_exec_session_control_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char session_id[MKGA_MAX_SESSION_ID_LEN];
};

struct mkga_exec_tty_resize_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char session_id[MKGA_MAX_SESSION_ID_LEN];
	uint32_t width;
	uint32_t height;
};

struct mkga_network_endpoint {
	char ip[MKGA_MAX_IP_LEN];
	uint16_t peer_kernel_id;
	uint16_t reserved0;
};

struct mkga_configure_network_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char pod_id[MKGA_MAX_POD_ID_LEN];
	char pod_ip[MKGA_MAX_IP_LEN];
	char pod_cidr[MKGA_MAX_CIDR_LEN];
	char mode[MKGA_MAX_MODE_LEN];
	uint32_t endpoint_count;
	uint32_t port_count;
	struct mkga_network_endpoint endpoints[MKGA_MAX_ENDPOINTS];
	uint16_t ports[MKGA_MAX_PORTS];
};

struct mkga_configure_env_req {
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	char pod_id[MKGA_MAX_POD_ID_LEN];
	char name[MKGA_MAX_NAME_LEN];
	char key[MKGA_MAX_ENV_KEY_LEN];
	char value[MKGA_MAX_ENV_VALUE_LEN];
};

union mkga_payload {
	struct mkga_create_container_req create_container_req;
	struct mkga_create_container_resp create_container_resp;
	struct mkga_container_control_req container_control_req;
	struct mkga_stop_container_resp stop_container_resp;
	struct mkga_container_status_resp container_status_resp;
	struct mkga_read_log_req read_log_req;
	struct mkga_read_log_resp read_log_resp;
	struct mkga_exec_tty_prepare_req exec_tty_prepare_req;
	struct mkga_exec_tty_prepare_resp exec_tty_prepare_resp;
	struct mkga_exec_session_control_req exec_session_control_req;
	struct mkga_exec_tty_resize_req exec_tty_resize_req;
	struct mkga_configure_network_req configure_network_req;
	struct mkga_configure_env_req configure_env_req;
};

struct mkga_envelope {
	int version;
	char id[MKGA_MAX_ID_LEN];
	enum mkga_message_kind kind;
	enum mkga_operation operation;
	uint16_t peer_kernel_id;
	uint64_t transport_request_id;
	char kernel_id[MKGA_MAX_KERNEL_ID_LEN];
	int64_t sent_at_millis;
	union mkga_payload payload;
	struct mkga_error error;
};

int64_t mkga_now_millis(void);
void mkga_envelope_init(struct mkga_envelope *env);
void mkga_envelope_make_response(const struct mkga_envelope *req,
				       struct mkga_envelope *resp);
void mkga_envelope_set_error(const struct mkga_envelope *req,
				  struct mkga_envelope *resp,
				  const char *code,
				  const char *message);
const char *mkga_operation_name(enum mkga_operation operation);

#endif
