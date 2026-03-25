#ifndef MKGA_RUNTIME_H
#define MKGA_RUNTIME_H

#include "mkga_protocol.h"

struct mkga_runtime;

struct mkga_runtime_ops {
	int (*create_container)(struct mkga_runtime *runtime,
				const struct mkga_create_container_req *req,
				struct mkga_create_container_resp *resp);
	int (*start_container)(struct mkga_runtime *runtime,
			       const struct mkga_container_control_req *req);
	int (*stop_container)(struct mkga_runtime *runtime,
			      const struct mkga_container_control_req *req,
			      struct mkga_stop_container_resp *resp);
	int (*remove_container)(struct mkga_runtime *runtime,
				const struct mkga_container_control_req *req);
	int (*status_container)(struct mkga_runtime *runtime,
				const struct mkga_container_control_req *req,
				struct mkga_container_status_resp *resp);
	int (*read_log)(struct mkga_runtime *runtime,
			const struct mkga_read_log_req *req,
			struct mkga_read_log_resp *resp);
	int (*exec_tty_prepare)(struct mkga_runtime *runtime,
				const struct mkga_exec_tty_prepare_req *req,
				struct mkga_exec_tty_prepare_resp *resp);
	int (*exec_tty_start)(struct mkga_runtime *runtime,
			      const struct mkga_exec_session_control_req *req);
	int (*exec_tty_resize)(struct mkga_runtime *runtime,
			       const struct mkga_exec_tty_resize_req *req);
	int (*exec_tty_close)(struct mkga_runtime *runtime,
			      const struct mkga_exec_session_control_req *req);
	void (*destroy)(struct mkga_runtime *runtime);
};

struct mkga_runtime {
	const struct mkga_runtime_ops *ops;
	void *impl;
};

int mkga_runtime_create_container(struct mkga_runtime *runtime,
				  const struct mkga_create_container_req *req,
				  struct mkga_create_container_resp *resp);
int mkga_runtime_start_container(struct mkga_runtime *runtime,
				 const struct mkga_container_control_req *req);
int mkga_runtime_stop_container(struct mkga_runtime *runtime,
				const struct mkga_container_control_req *req,
				struct mkga_stop_container_resp *resp);
int mkga_runtime_remove_container(struct mkga_runtime *runtime,
				  const struct mkga_container_control_req *req);
int mkga_runtime_status_container(struct mkga_runtime *runtime,
				  const struct mkga_container_control_req *req,
				  struct mkga_container_status_resp *resp);
int mkga_runtime_read_log(struct mkga_runtime *runtime,
			  const struct mkga_read_log_req *req,
			  struct mkga_read_log_resp *resp);
int mkga_runtime_exec_tty_prepare(struct mkga_runtime *runtime,
				  const struct mkga_exec_tty_prepare_req *req,
				  struct mkga_exec_tty_prepare_resp *resp);
int mkga_runtime_exec_tty_start(struct mkga_runtime *runtime,
				const struct mkga_exec_session_control_req *req);
int mkga_runtime_exec_tty_resize(struct mkga_runtime *runtime,
				 const struct mkga_exec_tty_resize_req *req);
int mkga_runtime_exec_tty_close(struct mkga_runtime *runtime,
				const struct mkga_exec_session_control_req *req);
void mkga_runtime_destroy(struct mkga_runtime *runtime);

struct mkga_runtime *mkga_memory_runtime_create(void);
struct mkga_runtime *mkga_containerd_runtime_create(const char *socket_path);

#endif
