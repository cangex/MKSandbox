#ifndef MKGA_SESSION_H
#define MKGA_SESSION_H

#include <pthread.h>
#include <stdbool.h>
#include <stddef.h>
#include <sys/types.h>

#include "mkga_protocol.h"

enum mkga_exec_session_state {
	MKGA_EXEC_SESSION_PREPARED = 1,
	MKGA_EXEC_SESSION_RUNNING = 2,
	MKGA_EXEC_SESSION_EXITED = 3,
	MKGA_EXEC_SESSION_CLOSED = 4,
};

struct mkga_exec_session {
	char session_id[MKGA_MAX_SESSION_ID_LEN];
	char container_id[MKGA_MAX_ID_LEN];
	char exec_id[MKGA_MAX_SESSION_ID_LEN];
	uint32_t argv_count;
	char argv[MKGA_MAX_ARGV][MKGA_MAX_ARG_LEN];
	bool tty;
	bool stdin_enabled;
	bool stdout_enabled;
	bool stderr_enabled;
	enum mkga_exec_session_state state;
	int master_fd;
	pid_t child_pid;
	int exit_code;
	pthread_t io_thread;
	pthread_t wait_thread;
	pthread_mutex_t lock;
	struct mkga_exec_session *next;
};

int mkga_session_prepare(const struct mkga_exec_tty_prepare_req *req,
			 struct mkga_exec_tty_prepare_resp *resp);
int mkga_session_start(const struct mkga_exec_session_control_req *req);
int mkga_session_resize(const struct mkga_exec_tty_resize_req *req);
int mkga_session_close(const struct mkga_exec_session_control_req *req);
struct mkga_exec_session *mkga_session_lookup(const char *session_id);
int mkga_session_write_stdin(const char *session_id, const uint8_t *data, size_t len);

#endif
