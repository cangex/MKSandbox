#ifndef _XOPEN_SOURCE
#define _XOPEN_SOURCE 600
#endif

#include "mkga_runtime.h"
#include "mkga_stream.h"
#include "session.h"

#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/poll.h>
#include <sys/stat.h>
#include <sys/time.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#define MKGA_CONTAINERD_DEFAULT_NAMESPACE "mk"
#define MKGA_CONTAINERD_DEFAULT_CTR_PATH "ctr"
#define MKGA_CONTAINERD_DEFAULT_RUNC_BINARY "/bin/runc"
#define MKGA_CONTAINERD_DEFAULT_STATE_ROOT "/run/mk-guest-agent/containerd"
#define MKGA_CONTAINERD_DEFAULT_TIMEOUT_MS 5000
#define MKGA_CONTAINERD_STOP_TERM_GRACE_MS 2000
#define MKGA_CONTAINERD_PULL_TIMEOUT_MS 30000
#define MKGA_CONTAINERD_READY_POLL_MS 200
#define MKGA_CONTAINERD_OUTPUT_MAX 4096
#define MKGA_CONTAINERD_ARGV_MAX 24
#define MKGA_CONTAINERD_CRI_LOG_BUFFER 4096

struct mkga_containerd_runtime {
	char socket_path[MKGA_MAX_LOG_PATH_LEN];
	char ctr_path[MKGA_MAX_LOG_PATH_LEN];
	char runc_binary[MKGA_MAX_LOG_PATH_LEN];
	char state_root[MKGA_MAX_LOG_PATH_LEN];
	char namespace_name[MKGA_MAX_NAME_LEN];
	int default_timeout_ms;
	bool no_pivot;
	bool start_via_run;
	bool null_io;
};

struct mkga_containerd_metadata {
	char image[MKGA_MAX_IMAGE_LEN];
	uint32_t argv_count;
	char argv[MKGA_MAX_ARGV][MKGA_MAX_ARG_LEN];
};

struct mkga_containerd_state {
	uint32_t state;
	int32_t exit_code;
	int32_t pid;
	uint64_t started_at_unix_nano;
	uint64_t finished_at_unix_nano;
	char message[MKGA_MAX_MESSAGE_LEN];
};

enum mkga_containerd_task_status {
	MKGA_CONTAINERD_TASK_STATUS_UNKNOWN = 0,
	MKGA_CONTAINERD_TASK_STATUS_RUNNING,
	MKGA_CONTAINERD_TASK_STATUS_STOPPED,
	MKGA_CONTAINERD_TASK_STATUS_MISSING,
};

struct mkga_cri_log_stream {
	const char *stream_name;
	uint8_t pending[MKGA_CONTAINERD_CRI_LOG_BUFFER];
	size_t pending_len;
};

struct mkga_exec_tty_waiter_arg {
	struct mkga_exec_session *session;
};

static int mkga_cri_log_flush_pending(int fd, struct mkga_cri_log_stream *stream,
				      char tag);
static int mkga_cri_log_append_chunk(int fd, struct mkga_cri_log_stream *stream,
				     const uint8_t *chunk, size_t chunk_len);
static void *mkga_containerd_exec_tty_output_pump(void *arg);
static void *mkga_containerd_exec_tty_waiter(void *arg);

static void mkga_copy_string(char *dst, size_t dst_len, const char *src)
{
	size_t copy_len;

	if (!dst || dst_len == 0) {
		return;
	}
	if (!src) {
		dst[0] = '\0';
		return;
	}

	copy_len = strlen(src);
	if (copy_len >= dst_len) {
		copy_len = dst_len - 1U;
	}
	memcpy(dst, src, copy_len);
	dst[copy_len] = '\0';
}

static int mkga_getenv_int(const char *key, int fallback)
{
	const char *value = getenv(key);
	char *end = NULL;
	long parsed;

	if (!value || value[0] == '\0') {
		return fallback;
	}

	parsed = strtol(value, &end, 10);
	if (!end || *end != '\0' || parsed <= 0) {
		return fallback;
	}
	return (int)parsed;
}

static bool mkga_getenv_bool(const char *key, bool fallback)
{
	const char *value = getenv(key);

	if (!value || value[0] == '\0') {
		return fallback;
	}
	if (strcmp(value, "1") == 0 || strcmp(value, "true") == 0 ||
	    strcmp(value, "yes") == 0 || strcmp(value, "on") == 0) {
		return true;
	}
	if (strcmp(value, "0") == 0 || strcmp(value, "false") == 0 ||
	    strcmp(value, "no") == 0 || strcmp(value, "off") == 0) {
		return false;
	}
	return fallback;
}

static int64_t mkga_now_monotonic_millis(void)
{
	struct timeval tv;

	gettimeofday(&tv, NULL);
	return (int64_t)tv.tv_sec * 1000LL + (int64_t)(tv.tv_usec / 1000);
}

static uint64_t mkga_now_realtime_nanos(void)
{
	struct timespec ts;

	if (clock_gettime(CLOCK_REALTIME, &ts) != 0) {
		return 0;
	}
	return (uint64_t)ts.tv_sec * 1000000000ULL + (uint64_t)ts.tv_nsec;
}

static int mkga_set_nonblock(int fd)
{
	int flags;

	flags = fcntl(fd, F_GETFL);
	if (flags < 0) {
		return -errno;
	}
	if (fcntl(fd, F_SETFL, flags | O_NONBLOCK) < 0) {
		return -errno;
	}
	return 0;
}

static void mkga_close_fd(int *fd)
{
	if (fd && *fd >= 0) {
		(void)close(*fd);
		*fd = -1;
	}
}

static void mkga_append_output(char *dst, size_t dst_len, size_t *used,
			       const char *src, size_t src_len)
{
	size_t room;
	size_t copy_len;

	if (!dst || dst_len == 0 || !used || !src || src_len == 0) {
		return;
	}

	if (*used >= dst_len - 1U) {
		dst[dst_len - 1U] = '\0';
		return;
	}

	room = dst_len - 1U - *used;
	copy_len = src_len < room ? src_len : room;
	memcpy(dst + *used, src, copy_len);
	*used += copy_len;
	dst[*used] = '\0';
}

static int mkga_read_pipe_into_buffer(int *fd, char *dst, size_t dst_len,
				      size_t *used, bool *open_flag)
{
	char buf[512];

	for (;;) {
		ssize_t nread;

		if (!fd || *fd < 0) {
			if (open_flag) {
				*open_flag = false;
			}
			return 0;
		}

		nread = read(*fd, buf, sizeof(buf));

		if (nread > 0) {
			mkga_append_output(dst, dst_len, used, buf, (size_t)nread);
			continue;
		}
		if (nread == 0) {
			mkga_close_fd(fd);
			if (open_flag) {
				*open_flag = false;
			}
			return 0;
		}
		if (errno == EINTR) {
			continue;
		}
		if (errno == EAGAIN || errno == EWOULDBLOCK) {
			return 0;
		}
		return -errno;
	}
}

static bool mkga_contains_text(const char *haystack, const char *needle)
{
	return haystack && needle && strstr(haystack, needle) != NULL;
}

static int mkga_map_ctr_error(int wait_status, const char *stderr_buf)
{
	if (WIFEXITED(wait_status)) {
		int code = WEXITSTATUS(wait_status);

		if (code == 0) {
			return 0;
		}
		if (code == 127) {
			return -ENOENT;
		}
	}

	if (mkga_contains_text(stderr_buf, "not found")) {
		return -ENOENT;
	}
	if (mkga_contains_text(stderr_buf, "already exists")) {
		return -EEXIST;
	}
	if (mkga_contains_text(stderr_buf, "deadline exceeded") ||
	    mkga_contains_text(stderr_buf, "timed out")) {
		return -ETIMEDOUT;
	}
	if (mkga_contains_text(stderr_buf, "permission denied")) {
		return -EACCES;
	}
	if (mkga_contains_text(stderr_buf, "invalid")) {
		return -EINVAL;
	}
	return -EIO;
}

static int mkga_ctr_exec(struct mkga_containerd_runtime *impl,
			 char *const argv[],
			 int timeout_ms,
			 char *stdout_buf,
			 size_t stdout_len,
			 char *stderr_buf,
			 size_t stderr_len)
{
	int stdout_pipe[2] = {-1, -1};
	int stderr_pipe[2] = {-1, -1};
	size_t stdout_used = 0;
	size_t stderr_used = 0;
	pid_t child;
	int child_status = 0;
	bool child_exited = false;
	bool stdout_open = true;
	bool stderr_open = true;
	int64_t start_ms = mkga_now_monotonic_millis();

	if (!impl || !argv || !argv[0]) {
		return -EINVAL;
	}

	if (stdout_buf && stdout_len > 0) {
		stdout_buf[0] = '\0';
	}
	if (stderr_buf && stderr_len > 0) {
		stderr_buf[0] = '\0';
	}

	if (pipe(stdout_pipe) != 0) {
		return -errno;
	}
	if (pipe(stderr_pipe) != 0) {
		int saved_errno = errno;
		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stdout_pipe[1]);
		return -saved_errno;
	}

	child = fork();
	if (child < 0) {
		int saved_errno = errno;
		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stdout_pipe[1]);
		mkga_close_fd(&stderr_pipe[0]);
		mkga_close_fd(&stderr_pipe[1]);
		return -saved_errno;
	}

	if (child == 0) {
		(void)dup2(stdout_pipe[1], STDOUT_FILENO);
		(void)dup2(stderr_pipe[1], STDERR_FILENO);
		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stdout_pipe[1]);
		mkga_close_fd(&stderr_pipe[0]);
		mkga_close_fd(&stderr_pipe[1]);
		execvp(argv[0], argv);
		_exit(errno == ENOENT ? 127 : 126);
	}

	mkga_close_fd(&stdout_pipe[1]);
	mkga_close_fd(&stderr_pipe[1]);

	if (mkga_set_nonblock(stdout_pipe[0]) != 0 ||
	    mkga_set_nonblock(stderr_pipe[0]) != 0) {
		(void)kill(child, SIGKILL);
		(void)waitpid(child, NULL, 0);
		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stderr_pipe[0]);
		return -EIO;
	}

	while (stdout_open || stderr_open || !child_exited) {
		struct pollfd pfds[2];
		nfds_t nfds = 0;
		int poll_timeout = MKGA_CONTAINERD_READY_POLL_MS;
		int rc;

		if (timeout_ms > 0) {
			int64_t elapsed = mkga_now_monotonic_millis() - start_ms;
			int64_t remaining = (int64_t)timeout_ms - elapsed;

			if (remaining <= 0) {
				(void)kill(child, SIGKILL);
				(void)waitpid(child, NULL, 0);
				mkga_close_fd(&stdout_pipe[0]);
				mkga_close_fd(&stderr_pipe[0]);
				return -ETIMEDOUT;
			}
			if (remaining < poll_timeout) {
				poll_timeout = (int)remaining;
			}
		}

		if (stdout_open) {
			pfds[nfds].fd = stdout_pipe[0];
			pfds[nfds].events = POLLIN | POLLHUP;
			pfds[nfds].revents = 0;
			nfds++;
		}
		if (stderr_open) {
			pfds[nfds].fd = stderr_pipe[0];
			pfds[nfds].events = POLLIN | POLLHUP;
			pfds[nfds].revents = 0;
			nfds++;
		}

		rc = poll(pfds, nfds, nfds == 0 ? 0 : poll_timeout);
		if (rc < 0) {
			if (errno == EINTR) {
				continue;
			}
			(void)kill(child, SIGKILL);
			(void)waitpid(child, NULL, 0);
			mkga_close_fd(&stdout_pipe[0]);
			mkga_close_fd(&stderr_pipe[0]);
			return -errno;
		}

		if (stdout_open) {
			rc = mkga_read_pipe_into_buffer(&stdout_pipe[0], stdout_buf,
							stdout_len, &stdout_used,
							&stdout_open);
			if (rc != 0) {
				(void)kill(child, SIGKILL);
				(void)waitpid(child, NULL, 0);
				mkga_close_fd(&stdout_pipe[0]);
				mkga_close_fd(&stderr_pipe[0]);
				return rc;
			}
		}
		if (stderr_open) {
			rc = mkga_read_pipe_into_buffer(&stderr_pipe[0], stderr_buf,
							stderr_len, &stderr_used,
							&stderr_open);
			if (rc != 0) {
				(void)kill(child, SIGKILL);
				(void)waitpid(child, NULL, 0);
				mkga_close_fd(&stdout_pipe[0]);
				mkga_close_fd(&stderr_pipe[0]);
				return rc;
			}
		}

		if (!child_exited) {
			pid_t w = waitpid(child, &child_status, WNOHANG);

			if (w == child) {
				child_exited = true;
			} else if (w < 0) {
				mkga_close_fd(&stdout_pipe[0]);
				mkga_close_fd(&stderr_pipe[0]);
				return -errno;
			}
		}
	}

	return mkga_map_ctr_error(child_status, stderr_buf);
}

static int mkga_ctr_exec_to_cri_log(struct mkga_containerd_runtime *impl,
				    char *const argv[],
				    const char *log_path,
				    int32_t *exit_code,
				    char *stderr_buf,
				    size_t stderr_len)
{
	int stdout_pipe[2] = {-1, -1};
	int stderr_pipe[2] = {-1, -1};
	size_t stderr_used = 0;
	pid_t child;
	int child_status = 0;
	bool child_exited = false;
	bool stdout_open = true;
	bool stderr_open = true;
	int log_fd = -1;
	struct mkga_cri_log_stream stdout_stream = {.stream_name = "stdout"};
	struct mkga_cri_log_stream stderr_stream = {.stream_name = "stderr"};

	if (!impl || !argv || !argv[0] || !log_path || !exit_code) {
		return -EINVAL;
	}
	*exit_code = 0;
	if (stderr_buf && stderr_len > 0) {
		stderr_buf[0] = '\0';
	}

	log_fd = open(log_path, O_WRONLY | O_CREAT | O_APPEND, 0644);
	if (log_fd < 0) {
		return -errno;
	}

	if (pipe(stdout_pipe) != 0) {
		mkga_close_fd(&log_fd);
		return -errno;
	}
	if (pipe(stderr_pipe) != 0) {
		int saved_errno = errno;

		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stdout_pipe[1]);
		mkga_close_fd(&log_fd);
		return -saved_errno;
	}

	child = fork();
	if (child < 0) {
		int saved_errno = errno;

		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stdout_pipe[1]);
		mkga_close_fd(&stderr_pipe[0]);
		mkga_close_fd(&stderr_pipe[1]);
		mkga_close_fd(&log_fd);
		return -saved_errno;
	}

	if (child == 0) {
		int null_fd = open("/dev/null", O_RDONLY);

		if (null_fd >= 0) {
			(void)dup2(null_fd, STDIN_FILENO);
			mkga_close_fd(&null_fd);
		}
		(void)dup2(stdout_pipe[1], STDOUT_FILENO);
		(void)dup2(stderr_pipe[1], STDERR_FILENO);
		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stdout_pipe[1]);
		mkga_close_fd(&stderr_pipe[0]);
		mkga_close_fd(&stderr_pipe[1]);
		mkga_close_fd(&log_fd);
		execvp(argv[0], argv);
		_exit(errno == ENOENT ? 127 : 126);
	}

	mkga_close_fd(&stdout_pipe[1]);
	mkga_close_fd(&stderr_pipe[1]);

	if (mkga_set_nonblock(stdout_pipe[0]) != 0 ||
	    mkga_set_nonblock(stderr_pipe[0]) != 0) {
		(void)kill(child, SIGKILL);
		(void)waitpid(child, NULL, 0);
		mkga_close_fd(&stdout_pipe[0]);
		mkga_close_fd(&stderr_pipe[0]);
		mkga_close_fd(&log_fd);
		return -EIO;
	}

	while (stdout_open || stderr_open || !child_exited) {
		struct pollfd pfds[2];
		nfds_t nfds = 0;
		int rc;

		if (stdout_open) {
			pfds[nfds].fd = stdout_pipe[0];
			pfds[nfds].events = POLLIN | POLLHUP;
			pfds[nfds].revents = 0;
			nfds++;
		}
		if (stderr_open) {
			pfds[nfds].fd = stderr_pipe[0];
			pfds[nfds].events = POLLIN | POLLHUP;
			pfds[nfds].revents = 0;
			nfds++;
		}

		rc = poll(pfds, nfds, nfds == 0 ? 0 : MKGA_CONTAINERD_READY_POLL_MS);
		if (rc < 0) {
			if (errno == EINTR) {
				continue;
			}
			(void)kill(child, SIGKILL);
			(void)waitpid(child, NULL, 0);
			mkga_close_fd(&stdout_pipe[0]);
			mkga_close_fd(&stderr_pipe[0]);
			mkga_close_fd(&log_fd);
			return -errno;
		}

		if (stdout_open) {
			for (;;) {
				uint8_t buf[512];
				ssize_t nread = read(stdout_pipe[0], buf, sizeof(buf));

				if (nread > 0) {
					rc = mkga_cri_log_append_chunk(log_fd, &stdout_stream, buf, (size_t)nread);
					if (rc != 0) {
						(void)kill(child, SIGKILL);
						(void)waitpid(child, NULL, 0);
						mkga_close_fd(&stdout_pipe[0]);
						mkga_close_fd(&stderr_pipe[0]);
						mkga_close_fd(&log_fd);
						return rc;
					}
					continue;
				}
				if (nread == 0) {
					mkga_close_fd(&stdout_pipe[0]);
					stdout_open = false;
				} else if (errno != EINTR && errno != EAGAIN && errno != EWOULDBLOCK) {
					(void)kill(child, SIGKILL);
					(void)waitpid(child, NULL, 0);
					mkga_close_fd(&stdout_pipe[0]);
					mkga_close_fd(&stderr_pipe[0]);
					mkga_close_fd(&log_fd);
					return -errno;
				}
				break;
			}
		}

		if (stderr_open) {
			for (;;) {
				uint8_t buf[512];
				ssize_t nread = read(stderr_pipe[0], buf, sizeof(buf));

				if (nread > 0) {
					mkga_append_output(stderr_buf, stderr_len, &stderr_used, (const char *)buf,
						       (size_t)nread);
					rc = mkga_cri_log_append_chunk(log_fd, &stderr_stream, buf, (size_t)nread);
					if (rc != 0) {
						(void)kill(child, SIGKILL);
						(void)waitpid(child, NULL, 0);
						mkga_close_fd(&stdout_pipe[0]);
						mkga_close_fd(&stderr_pipe[0]);
						mkga_close_fd(&log_fd);
						return rc;
					}
					continue;
				}
				if (nread == 0) {
					mkga_close_fd(&stderr_pipe[0]);
					stderr_open = false;
				} else if (errno != EINTR && errno != EAGAIN && errno != EWOULDBLOCK) {
					(void)kill(child, SIGKILL);
					(void)waitpid(child, NULL, 0);
					mkga_close_fd(&stdout_pipe[0]);
					mkga_close_fd(&stderr_pipe[0]);
					mkga_close_fd(&log_fd);
					return -errno;
				}
				break;
			}
		}

		if (!child_exited) {
			pid_t w = waitpid(child, &child_status, WNOHANG);

			if (w == child) {
				child_exited = true;
			} else if (w < 0) {
				mkga_close_fd(&stdout_pipe[0]);
				mkga_close_fd(&stderr_pipe[0]);
				mkga_close_fd(&log_fd);
				return -errno;
			}
		}
	}

	if (mkga_cri_log_flush_pending(log_fd, &stdout_stream, 'F') != 0 ||
	    mkga_cri_log_flush_pending(log_fd, &stderr_stream, 'F') != 0) {
		mkga_close_fd(&log_fd);
		return -EIO;
	}
	if (close(log_fd) != 0) {
		return -errno;
	}
	log_fd = -1;

	if (WIFEXITED(child_status)) {
		*exit_code = WEXITSTATUS(child_status);
		return *exit_code == 0 ? 0 : -EIO;
	}
	if (WIFSIGNALED(child_status)) {
		*exit_code = 128 + WTERMSIG(child_status);
		return -EIO;
	}
	return -EIO;
}

static int mkga_ctr_fill_base_args(struct mkga_containerd_runtime *impl,
				   char *argv[],
				   size_t cap)
{
	if (!impl || !argv || cap < 6U) {
		return -EINVAL;
	}

	argv[0] = impl->ctr_path;
	argv[1] = "--address";
	argv[2] = impl->socket_path;
	argv[3] = "--namespace";
	argv[4] = impl->namespace_name;
	return 5;
}

static int mkga_containerd_effective_timeout(struct mkga_containerd_runtime *impl,
					     int64_t requested_ms,
					     int fallback_ms)
{
	if (requested_ms > 0 && requested_ms <= INT32_MAX) {
		return (int)requested_ms;
	}
	if (fallback_ms > 0) {
		return fallback_ms;
	}
	return impl ? impl->default_timeout_ms : MKGA_CONTAINERD_DEFAULT_TIMEOUT_MS;
}

static int mkga_containerd_ensure_state_root(struct mkga_containerd_runtime *impl)
{
	char path[MKGA_MAX_LOG_PATH_LEN];
	size_t i;

	if (!impl || impl->state_root[0] == '\0') {
		return -EINVAL;
	}

	mkga_copy_string(path, sizeof(path), impl->state_root);
	for (i = 1; path[i] != '\0'; i++) {
		if (path[i] != '/') {
			continue;
		}
		path[i] = '\0';
		if (mkdir(path, 0755) != 0 && errno != EEXIST) {
			return -errno;
		}
		path[i] = '/';
	}
	if (mkdir(path, 0755) != 0 && errno != EEXIST) {
		return -errno;
	}
	return 0;
}

static int mkga_containerd_metadata_path(struct mkga_containerd_runtime *impl,
					 const char *container_id,
					 char *path,
					 size_t path_len)
{
	int rc;

	if (!impl || !container_id || !path || path_len == 0) {
		return -EINVAL;
	}

	rc = mkga_containerd_ensure_state_root(impl);
	if (rc != 0) {
		return rc;
	}

	if (snprintf(path, path_len, "%s/%s.meta", impl->state_root, container_id) >=
	    (int)path_len) {
		return -ENOSPC;
	}
	return 0;
}

static int mkga_containerd_state_path(struct mkga_containerd_runtime *impl,
				      const char *container_id,
				      char *path,
				      size_t path_len)
{
	int rc;

	if (!impl || !container_id || !path || path_len == 0) {
		return -EINVAL;
	}

	rc = mkga_containerd_ensure_state_root(impl);
	if (rc != 0) {
		return rc;
	}

	if (snprintf(path, path_len, "%s/%s.state", impl->state_root, container_id) >=
	    (int)path_len) {
		return -ENOSPC;
	}
	return 0;
}

static int mkga_containerd_log_path(struct mkga_containerd_runtime *impl,
				    const char *container_id,
				    char *path,
				    size_t path_len)
{
	int rc;

	if (!impl || !container_id || !path || path_len == 0) {
		return -EINVAL;
	}

	rc = mkga_containerd_ensure_state_root(impl);
	if (rc != 0) {
		return rc;
	}

	if (snprintf(path, path_len, "%s/%s.cri.log", impl->state_root, container_id) >=
	    (int)path_len) {
		return -ENOSPC;
	}
	return 0;
}

static int mkga_containerd_write_metadata(struct mkga_containerd_runtime *impl,
					  const char *container_id,
					  const struct mkga_containerd_metadata *meta)
{
	char path[MKGA_MAX_LOG_PATH_LEN];
	FILE *fp = NULL;
	int rc;

	if (!meta) {
		return -EINVAL;
	}

	rc = mkga_containerd_metadata_path(impl, container_id, path, sizeof(path));
	if (rc != 0) {
		return rc;
	}

	fp = fopen(path, "w");
	if (!fp) {
		return -errno;
	}

	if (fprintf(fp, "%s\n%u\n", meta->image, meta->argv_count) < 0) {
		rc = -EIO;
		(void)fclose(fp);
		return rc;
	}
	for (uint32_t i = 0; i < meta->argv_count; i++) {
		if (fprintf(fp, "%s\n", meta->argv[i]) < 0) {
			rc = -EIO;
			(void)fclose(fp);
			return rc;
		}
	}
	if (fclose(fp) != 0) {
		return -errno;
	}

	return 0;
}

static int mkga_containerd_read_metadata(struct mkga_containerd_runtime *impl,
					 const char *container_id,
					 struct mkga_containerd_metadata *meta)
{
	char path[MKGA_MAX_LOG_PATH_LEN];
	FILE *fp = NULL;
	char line[MKGA_MAX_IMAGE_LEN];
	char count_line[32];
	char *newline;
	int rc;

	if (!meta) {
		return -EINVAL;
	}

	rc = mkga_containerd_metadata_path(impl, container_id, path, sizeof(path));
	if (rc != 0) {
		return rc;
	}

	fp = fopen(path, "r");
	if (!fp) {
		return -errno;
	}

	memset(meta, 0, sizeof(*meta));
	if (!fgets(line, sizeof(line), fp)) {
		rc = ferror(fp) ? -EIO : -ENOENT;
		(void)fclose(fp);
		return rc;
	}
	newline = strchr(line, '\n');
	if (newline) {
		*newline = '\0';
	}
	mkga_copy_string(meta->image, sizeof(meta->image), line);
	if (!fgets(count_line, sizeof(count_line), fp)) {
		rc = ferror(fp) ? -EIO : -ENOENT;
		(void)fclose(fp);
		return rc;
	}
	meta->argv_count = (uint32_t)strtoul(count_line, NULL, 10);
	if (meta->argv_count > MKGA_MAX_ARGV) {
		(void)fclose(fp);
		return -EINVAL;
	}
	for (uint32_t i = 0; i < meta->argv_count; i++) {
		char arg_line[MKGA_MAX_ARG_LEN];

		if (!fgets(arg_line, sizeof(arg_line), fp)) {
			rc = ferror(fp) ? -EIO : -ENOENT;
			(void)fclose(fp);
			return rc;
		}
		newline = strchr(arg_line, '\n');
		if (newline) {
			*newline = '\0';
		}
		mkga_copy_string(meta->argv[i], sizeof(meta->argv[i]), arg_line);
	}

	if (fclose(fp) != 0) {
		return -errno;
	}

	return 0;
}

static void mkga_containerd_delete_metadata(struct mkga_containerd_runtime *impl,
					    const char *container_id)
{
	char path[MKGA_MAX_LOG_PATH_LEN];

	if (mkga_containerd_metadata_path(impl, container_id, path, sizeof(path)) != 0) {
		return;
	}
	(void)unlink(path);
}

static int mkga_containerd_write_state(struct mkga_containerd_runtime *impl,
				       const char *container_id,
				       const struct mkga_containerd_state *state)
{
	char path[MKGA_MAX_LOG_PATH_LEN];
	int fd = -1;
	ssize_t nwritten;
	int rc;

	if (!state) {
		return -EINVAL;
	}

	rc = mkga_containerd_state_path(impl, container_id, path, sizeof(path));
	if (rc != 0) {
		return rc;
	}

	fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0644);
	if (fd < 0) {
		return -errno;
	}

	nwritten = write(fd, state, sizeof(*state));
	if (nwritten != (ssize_t)sizeof(*state)) {
		rc = nwritten < 0 ? -errno : -EIO;
		(void)close(fd);
		return rc;
	}
	if (close(fd) != 0) {
		return -errno;
	}
	return 0;
}

static int mkga_containerd_read_state(struct mkga_containerd_runtime *impl,
				      const char *container_id,
				      struct mkga_containerd_state *state)
{
	char path[MKGA_MAX_LOG_PATH_LEN];
	int fd = -1;
	ssize_t nread;
	int rc;

	if (!state) {
		return -EINVAL;
	}

	rc = mkga_containerd_state_path(impl, container_id, path, sizeof(path));
	if (rc != 0) {
		return rc;
	}

	fd = open(path, O_RDONLY);
	if (fd < 0) {
		return -errno;
	}

	nread = read(fd, state, sizeof(*state));
	if (nread != (ssize_t)sizeof(*state)) {
		rc = nread < 0 ? -errno : -EIO;
		(void)close(fd);
		return rc;
	}
	if (close(fd) != 0) {
		return -errno;
	}
	return 0;
}

static void mkga_containerd_delete_state(struct mkga_containerd_runtime *impl,
					 const char *container_id)
{
	char path[MKGA_MAX_LOG_PATH_LEN];

	if (mkga_containerd_state_path(impl, container_id, path, sizeof(path)) != 0) {
		return;
	}
	(void)unlink(path);
}

static void mkga_containerd_delete_log(struct mkga_containerd_runtime *impl,
				       const char *container_id)
{
	char path[MKGA_MAX_LOG_PATH_LEN];

	if (mkga_containerd_log_path(impl, container_id, path, sizeof(path)) != 0) {
		return;
	}
	(void)unlink(path);
}

static int mkga_write_all(int fd, const void *buf, size_t len)
{
	const uint8_t *cursor = buf;
	size_t written = 0;

	while (written < len) {
		ssize_t nwritten = write(fd, cursor + written, len - written);

		if (nwritten > 0) {
			written += (size_t)nwritten;
			continue;
		}
		if (nwritten < 0 && errno == EINTR) {
			continue;
		}
		return -errno;
	}
	return 0;
}

static int mkga_cri_log_write_record(int fd, const char *stream_name, char tag,
				     const uint8_t *payload, size_t payload_len)
{
	struct timespec ts;
	struct tm tm;
	char header[128];
	int header_len;
	int rc;

	if (fd < 0 || !stream_name || !payload) {
		return -EINVAL;
	}
	if (clock_gettime(CLOCK_REALTIME, &ts) != 0) {
		return -errno;
	}
	if (!gmtime_r(&ts.tv_sec, &tm)) {
		return -errno;
	}

	header_len = snprintf(header, sizeof(header),
			      "%04d-%02d-%02dT%02d:%02d:%02d.%09ldZ %s %c ",
			      tm.tm_year + 1900, tm.tm_mon + 1, tm.tm_mday,
			      tm.tm_hour, tm.tm_min, tm.tm_sec, ts.tv_nsec,
			      stream_name, tag);
	if (header_len < 0 || header_len >= (int)sizeof(header)) {
		return -ENOSPC;
	}

	rc = mkga_write_all(fd, header, (size_t)header_len);
	if (rc != 0) {
		return rc;
	}
	if (payload_len > 0) {
		rc = mkga_write_all(fd, payload, payload_len);
		if (rc != 0) {
			return rc;
		}
	}
	return mkga_write_all(fd, "\n", 1);
}

static int mkga_cri_log_flush_pending(int fd, struct mkga_cri_log_stream *stream, char tag)
{
	int rc;

	if (!stream || stream->pending_len == 0) {
		return 0;
	}
	rc = mkga_cri_log_write_record(fd, stream->stream_name, tag, stream->pending,
				       stream->pending_len);
	if (rc != 0) {
		return rc;
	}
	stream->pending_len = 0;
	return 0;
}

static int mkga_cri_log_append_chunk(int fd, struct mkga_cri_log_stream *stream,
				     const uint8_t *chunk, size_t chunk_len)
{
	size_t i;

	if (!stream || (!chunk && chunk_len != 0)) {
		return -EINVAL;
	}

	for (i = 0; i < chunk_len; i++) {
		uint8_t ch = chunk[i];
		int rc;

		if (ch == '\n') {
			rc = mkga_cri_log_flush_pending(fd, stream, 'F');
			if (rc != 0) {
				return rc;
			}
			continue;
		}
		if (stream->pending_len >= sizeof(stream->pending)) {
			rc = mkga_cri_log_flush_pending(fd, stream, 'P');
			if (rc != 0) {
				return rc;
			}
		}
		stream->pending[stream->pending_len++] = ch;
	}
	return 0;
}

static uint64_t mkga_fnv1a64_update(uint64_t hash, const char *text)
{
	size_t i;

	if (!text) {
		return hash;
	}
	for (i = 0; text[i] != '\0'; i++) {
		hash ^= (unsigned char)text[i];
		hash *= 1099511628211ULL;
	}
	hash ^= 0xffU;
	hash *= 1099511628211ULL;
	return hash;
}

static int mkga_containerd_make_container_id(const struct mkga_create_container_req *req,
					     char *out_id, size_t out_len)
{
	uint64_t hash = 1469598103934665603ULL;

	if (!req || !out_id || out_len == 0) {
		return -EINVAL;
	}

	hash = mkga_fnv1a64_update(hash, req->kernel_id);
	hash = mkga_fnv1a64_update(hash, req->pod_id);
	hash = mkga_fnv1a64_update(hash, req->name);
	hash = mkga_fnv1a64_update(hash, req->image);
	for (uint32_t i = 0; i < req->argv_count && i < MKGA_MAX_ARGV; i++) {
		hash = mkga_fnv1a64_update(hash, req->argv[i]);
	}

	if (snprintf(out_id, out_len, "mkc-%016llx",
		     (unsigned long long)hash) >= (int)out_len) {
		return -ENOSPC;
	}
	return 0;
}

static void mkga_containerd_fill_state(struct mkga_containerd_state *state,
				       uint32_t container_state,
				       int32_t exit_code,
				       int32_t pid,
				       uint64_t started_at_unix_nano,
				       uint64_t finished_at_unix_nano,
				       const char *message)
{
	if (!state) {
		return;
	}

	memset(state, 0, sizeof(*state));
	state->state = container_state;
	state->exit_code = exit_code;
	state->pid = pid;
	state->started_at_unix_nano = started_at_unix_nano;
	state->finished_at_unix_nano = finished_at_unix_nano;
	mkga_copy_string(state->message, sizeof(state->message), message);
}

static int mkga_containerd_validate_create_req(const struct mkga_create_container_req *req)
{
	if (!req) {
		return -EINVAL;
	}
	if (req->kernel_id[0] == '\0' || req->pod_id[0] == '\0' ||
	    req->name[0] == '\0' || req->image[0] == '\0') {
		return -EINVAL;
	}
	if (req->argv_count > MKGA_MAX_ARGV) {
		return -EINVAL;
	}
	for (uint32_t i = 0; i < req->argv_count; i++) {
		if (req->argv[i][0] == '\0') {
			return -EINVAL;
		}
	}
	return 0;
}

static int mkga_containerd_append_exec_argv(char *argv[],
					    int argc,
					    int argv_max,
					    uint32_t exec_argc,
					    char exec_argv[MKGA_MAX_ARGV][MKGA_MAX_ARG_LEN])
{
	if (!argv || argc < 0 || argv_max <= 0) {
		return -EINVAL;
	}
	if (exec_argc > MKGA_MAX_ARGV) {
		return -EINVAL;
	}
	for (uint32_t i = 0; i < exec_argc; i++) {
		if (argc >= argv_max - 1) {
			return -E2BIG;
		}
		argv[argc++] = (char *)exec_argv[i];
	}
	return argc;
}

static int mkga_containerd_validate_control_req(const struct mkga_container_control_req *req)
{
	if (!req) {
		return -EINVAL;
	}
	if (req->kernel_id[0] == '\0' || req->container_id[0] == '\0') {
		return -EINVAL;
	}
	return 0;
}

static int mkga_containerd_validate_read_log_req(const struct mkga_read_log_req *req)
{
	if (!req) {
		return -EINVAL;
	}
	if (req->kernel_id[0] == '\0' || req->container_id[0] == '\0') {
		return -EINVAL;
	}
	return 0;
}

static int mkga_containerd_build_exec_tty_argv(struct mkga_containerd_runtime *impl,
					       const struct mkga_exec_session *session,
					       char *argv[],
					       size_t argv_cap)
{
	int argc;

	if (!impl || !session || !argv || argv_cap == 0) {
		return -EINVAL;
	}
	if (session->argv_count == 0 || session->argv_count > MKGA_MAX_ARGV) {
		return -EINVAL;
	}

	memset(argv, 0, sizeof(char *) * argv_cap);
	argc = mkga_ctr_fill_base_args(impl, argv, argv_cap);
	if (argc < 0) {
		return argc;
	}

	argv[argc++] = "tasks";
	argv[argc++] = "exec";
	argv[argc++] = "--exec-id";
	argv[argc++] = (char *)session->exec_id;
	if (session->tty) {
		argv[argc++] = "--tty";
	}
	argv[argc++] = (char *)session->container_id;
	argc = mkga_containerd_append_exec_argv(argv, argc, (int)argv_cap,
					       session->argv_count,
					       (char (*)[MKGA_MAX_ARG_LEN])session->argv);
	if (argc < 0) {
		return argc;
	}
	argv[argc] = NULL;
	return argc;
}

static void *mkga_containerd_exec_tty_output_pump(void *arg)
{
	struct mkga_exec_session *session = arg;
	uint8_t buf[MKRING_STREAM_MAX_PAYLOAD];

	if (!session) {
		return NULL;
	}

	for (;;) {
		struct pollfd pfd;
		int master_fd;
		int state;
		int rc;
		ssize_t nread;

		pthread_mutex_lock(&session->lock);
		master_fd = session->master_fd;
		state = session->state;
		pthread_mutex_unlock(&session->lock);

		if (master_fd < 0 || state == MKGA_EXEC_SESSION_EXITED ||
		    state == MKGA_EXEC_SESSION_CLOSED) {
			return NULL;
		}

		memset(&pfd, 0, sizeof(pfd));
		pfd.fd = master_fd;
		pfd.events = POLLIN | POLLHUP;

		rc = poll(&pfd, 1, 250);
		if (rc < 0) {
			if (errno == EINTR) {
				continue;
			}
			return NULL;
		}
		if (rc == 0) {
			continue;
		}
		if ((pfd.revents & POLLIN) == 0) {
			if ((pfd.revents & POLLHUP) != 0) {
				return NULL;
			}
			continue;
		}

		nread = read(master_fd, buf, sizeof(buf));
		if (nread > 0) {
			(void)fprintf(stderr,
				      "mkga exec_tty_output session=%s bytes=%zd\n",
				      session->session_id,
				      nread);
			(void)mkga_stream_send_output(session->session_id, buf, (size_t)nread);
			continue;
		}
		if (nread == 0) {
			return NULL;
		}
		if (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) {
			continue;
		}
		return NULL;
	}
}

static void *mkga_containerd_exec_tty_waiter(void *arg)
{
	struct mkga_exec_tty_waiter_arg *waiter = arg;
	struct mkga_exec_session *session;
	int status = 0;
	int exit_code = 0;

	if (!waiter) {
		return NULL;
	}
	session = waiter->session;
	free(waiter);
	if (!session) {
		return NULL;
	}

	for (;;) {
		pid_t rc;

		rc = waitpid(session->child_pid, &status, 0);
		if (rc >= 0) {
			break;
		}
		if (errno != EINTR) {
			status = 0;
			break;
		}
	}

	if (WIFEXITED(status)) {
		exit_code = WEXITSTATUS(status);
	} else if (WIFSIGNALED(status)) {
		exit_code = 128 + WTERMSIG(status);
	}
	(void)fprintf(stderr,
		      "mkga exec_tty_waiter session=%s exit_code=%d status=0x%x\n",
		      session->session_id,
		      exit_code,
		      status);

	pthread_mutex_lock(&session->lock);
	if (session->master_fd >= 0) {
		(void)close(session->master_fd);
		session->master_fd = -1;
	}
	session->child_pid = -1;
	session->exit_code = exit_code;
	if (session->state != MKGA_EXEC_SESSION_CLOSED) {
		session->state = MKGA_EXEC_SESSION_EXITED;
	}
	pthread_mutex_unlock(&session->lock);
	(void)mkga_stream_send_exit(session->session_id, exit_code);
	return NULL;
}

static bool mkga_containerd_image_present(struct mkga_containerd_runtime *impl,
					  const char *image)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;
	int rc;

	if (!impl || !image || image[0] == '\0') {
		return false;
	}

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return false;
	}
	argv[argc++] = "images";
	argv[argc++] = "ls";
	argv[argc] = NULL;

	rc = mkga_ctr_exec(impl, argv, impl->default_timeout_ms,
			   stdout_buf, sizeof(stdout_buf),
			   stderr_buf, sizeof(stderr_buf));
	if (rc != 0) {
		return false;
	}

	return mkga_contains_text(stdout_buf, image);
}

static int mkga_containerd_pull_image(struct mkga_containerd_runtime *impl,
				      const char *image)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "images";
	argv[argc++] = "pull";
	argv[argc++] = (char *)image;
	argv[argc] = NULL;

	return mkga_ctr_exec(impl, argv, MKGA_CONTAINERD_PULL_TIMEOUT_MS,
			     stdout_buf, sizeof(stdout_buf),
			     stderr_buf, sizeof(stderr_buf));
}

static int mkga_containerd_ensure_image(struct mkga_containerd_runtime *impl,
					const char *image)
{
	if (mkga_containerd_image_present(impl, image)) {
		return 0;
	}
	return mkga_containerd_pull_image(impl, image);
}

static int mkga_containerd_ping(struct mkga_containerd_runtime *impl, int timeout_ms)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;

	if (!impl) {
		return -EINVAL;
	}

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "version";
	argv[argc] = NULL;

	return mkga_ctr_exec(impl, argv,
			     mkga_containerd_effective_timeout(impl, timeout_ms,
							      impl->default_timeout_ms),
			     stdout_buf, sizeof(stdout_buf),
			     stderr_buf, sizeof(stderr_buf));
}

static int mkga_containerd_wait_ready(struct mkga_containerd_runtime *impl,
				      int timeout_ms)
{
	int effective_timeout;
	int64_t deadline;
	int last_rc = -ETIMEDOUT;

	if (!impl) {
		return -EINVAL;
	}

	effective_timeout = mkga_containerd_effective_timeout(
		impl, timeout_ms, impl->default_timeout_ms);
	deadline = mkga_now_monotonic_millis() + effective_timeout;

	while (mkga_now_monotonic_millis() < deadline) {
		last_rc = mkga_containerd_ping(impl, MKGA_CONTAINERD_READY_POLL_MS);
		if (last_rc == 0) {
			return 0;
		}
		usleep((useconds_t)MKGA_CONTAINERD_READY_POLL_MS * 1000U);
	}

	return last_rc == 0 ? -ETIMEDOUT : last_rc;
}

static int mkga_parse_exit_code_text(const char *text, int32_t *exit_code)
{
	const char *cursor;

	if (!text || !exit_code) {
		return -EINVAL;
	}

	for (cursor = text; *cursor != '\0'; cursor++) {
		if ((*cursor >= '0' && *cursor <= '9') ||
		    (*cursor == '-' && cursor[1] >= '0' && cursor[1] <= '9')) {
			char *end = NULL;
			long value = strtol(cursor, &end, 10);

			if (end && end != cursor) {
				*exit_code = (int32_t)value;
				return 0;
			}
		}
	}
	return -EINVAL;
}

static bool mkga_ctr_wait_unsupported(const char *stdout_buf, const char *stderr_buf)
{
	return mkga_contains_text(stdout_buf, "No help topic for 'wait'") ||
	       mkga_contains_text(stderr_buf, "No help topic for 'wait'") ||
	       mkga_contains_text(stdout_buf, "No help topic for \"wait\"") ||
	       mkga_contains_text(stderr_buf, "No help topic for \"wait\"");
}

static int mkga_containerd_query_task_status(struct mkga_containerd_runtime *impl,
					     const char *container_id,
					     enum mkga_containerd_task_status *task_status,
					     int32_t *pid)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char *line;
	char *saveptr = NULL;
	int argc;
	int rc;

	if (!impl || !container_id || !task_status || !pid) {
		return -EINVAL;
	}

	*task_status = MKGA_CONTAINERD_TASK_STATUS_MISSING;
	*pid = 0;

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "tasks";
	argv[argc++] = "ls";
	argv[argc] = NULL;

	rc = mkga_ctr_exec(impl, argv, impl->default_timeout_ms,
			   stdout_buf, sizeof(stdout_buf),
			   stderr_buf, sizeof(stderr_buf));
	if (rc != 0) {
		return rc;
	}

	line = strtok_r(stdout_buf, "\n", &saveptr);
	while (line) {
		char line_copy[MKGA_CONTAINERD_OUTPUT_MAX];
		char *tok_save = NULL;
		char *task_id;
		char *pid_token;
		char *status_token;

		mkga_copy_string(line_copy, sizeof(line_copy), line);
		task_id = strtok_r(line_copy, " \t\r", &tok_save);
		if (!task_id) {
			line = strtok_r(NULL, "\n", &saveptr);
			continue;
		}
		if (strcmp(task_id, "TASK") == 0) {
			line = strtok_r(NULL, "\n", &saveptr);
			continue;
		}
		pid_token = strtok_r(NULL, " \t\r", &tok_save);
		status_token = strtok_r(NULL, " \t\r", &tok_save);
		if (strcmp(task_id, container_id) != 0) {
			line = strtok_r(NULL, "\n", &saveptr);
			continue;
		}

		if (pid_token && pid_token[0] != '\0' && strcmp(pid_token, "-") != 0) {
			*pid = (int32_t)strtol(pid_token, NULL, 10);
		}
		if (!status_token) {
			*task_status = MKGA_CONTAINERD_TASK_STATUS_UNKNOWN;
			return 0;
		}
		if (strcmp(status_token, "RUNNING") == 0) {
			*task_status = MKGA_CONTAINERD_TASK_STATUS_RUNNING;
			return 0;
		}
		if (strcmp(status_token, "STOPPED") == 0) {
			*task_status = MKGA_CONTAINERD_TASK_STATUS_STOPPED;
			return 0;
		}
		*task_status = MKGA_CONTAINERD_TASK_STATUS_UNKNOWN;
		return 0;
	}

	return 0;
}

static int mkga_containerd_poll_for_exit_until(struct mkga_containerd_runtime *impl,
					       const char *container_id,
					       int64_t deadline_ms,
					       int32_t *exit_code)
{
	if (!impl || !container_id || !exit_code) {
		return -EINVAL;
	}

	for (;;) {
		enum mkga_containerd_task_status task_status = MKGA_CONTAINERD_TASK_STATUS_UNKNOWN;
		int32_t pid = 0;
		int rc = mkga_containerd_query_task_status(impl, container_id, &task_status, &pid);

		if (rc != 0) {
			return rc;
		}
		if (task_status == MKGA_CONTAINERD_TASK_STATUS_STOPPED ||
		    task_status == MKGA_CONTAINERD_TASK_STATUS_MISSING) {
			*exit_code = 0;
			return 0;
		}
		if (task_status == MKGA_CONTAINERD_TASK_STATUS_UNKNOWN) {
			return -EIO;
		}

		if (mkga_now_monotonic_millis() >= deadline_ms) {
			return -ETIMEDOUT;
		}
		usleep(200U * 1000U);
	}
}

static int mkga_containerd_wait_for_exit_until(struct mkga_containerd_runtime *impl,
					       const char *container_id,
					       int64_t deadline_ms,
					       int32_t *exit_code)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;
	int64_t remaining_ms;
	int rc;

	if (!impl || !container_id || !exit_code) {
		return -EINVAL;
	}

	remaining_ms = deadline_ms - mkga_now_monotonic_millis();
	if (remaining_ms <= 0) {
		return mkga_containerd_poll_for_exit_until(impl, container_id, deadline_ms, exit_code);
	}

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "tasks";
	argv[argc++] = "wait";
	argv[argc++] = (char *)container_id;
	argv[argc] = NULL;

	rc = mkga_ctr_exec(impl, argv, (int)remaining_ms, stdout_buf, sizeof(stdout_buf),
			   stderr_buf, sizeof(stderr_buf));
	if (rc != 0) {
		if (mkga_ctr_wait_unsupported(stdout_buf, stderr_buf)) {
			return mkga_containerd_poll_for_exit_until(impl, container_id, deadline_ms,
								   exit_code);
		}
		return rc;
	}

	rc = mkga_parse_exit_code_text(stdout_buf, exit_code);
	if (rc == 0) {
		return 0;
	}
	rc = mkga_parse_exit_code_text(stderr_buf, exit_code);
	if (rc == 0) {
		return 0;
	}

	*exit_code = 0;
	return 0;
}

static int mkga_containerd_wait_for_exit(struct mkga_containerd_runtime *impl,
					 const char *container_id,
					 int timeout_ms,
					 int32_t *exit_code)
{
	int effective_timeout;
	int64_t deadline;

	if (!impl || !container_id || !exit_code) {
		return -EINVAL;
	}

	effective_timeout = mkga_containerd_effective_timeout(
		impl, timeout_ms, impl->default_timeout_ms);
	deadline = mkga_now_monotonic_millis() + effective_timeout;
	return mkga_containerd_wait_for_exit_until(impl, container_id, deadline, exit_code);
}

static int mkga_containerd_run_logged_foreground(struct mkga_containerd_runtime *impl,
						 const char *container_id,
						 const char *image,
						 uint32_t argv_count,
						 char argv_items[MKGA_MAX_ARGV][MKGA_MAX_ARG_LEN],
						 const char *log_path)
{
	struct mkga_containerd_state state;
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;
	int32_t exit_code = 0;
	int rc;

	if (!impl || !container_id || !image || !log_path) {
		return -EINVAL;
	}

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "run";
	if (impl->no_pivot) {
		argv[argc++] = "--no-pivot";
	}
	if (impl->runc_binary[0] != '\0') {
		argv[argc++] = "--runc-binary";
		argv[argc++] = impl->runc_binary;
	}
	argv[argc++] = (char *)image;
	argv[argc++] = (char *)container_id;
	argc = mkga_containerd_append_exec_argv(argv, argc, MKGA_CONTAINERD_ARGV_MAX,
					       argv_count, argv_items);
	if (argc < 0) {
		return argc;
	}
	argv[argc] = NULL;

	rc = mkga_ctr_exec_to_cri_log(impl, argv, log_path, &exit_code, stderr_buf,
				      sizeof(stderr_buf));
	if (mkga_containerd_read_state(impl, container_id, &state) != 0) {
		mkga_containerd_fill_state(&state, MKGA_CONTAINER_STATE_EXITED, exit_code, 0,
					   0, mkga_now_realtime_nanos(), stderr_buf);
	} else {
		state.state = MKGA_CONTAINER_STATE_EXITED;
		state.exit_code = exit_code;
		state.pid = 0;
		if (state.started_at_unix_nano == 0) {
			state.started_at_unix_nano = mkga_now_realtime_nanos();
		}
		state.finished_at_unix_nano = mkga_now_realtime_nanos();
		mkga_copy_string(state.message, sizeof(state.message), stderr_buf);
	}
	(void)mkga_containerd_write_state(impl, container_id, &state);
	return rc;
}

static int mkga_containerd_spawn_log_runner(struct mkga_containerd_runtime *impl,
					    const char *container_id,
					    const char *image,
					    uint32_t argv_count,
					    char argv_items[MKGA_MAX_ARGV][MKGA_MAX_ARG_LEN])
{
	char log_path[MKGA_MAX_LOG_PATH_LEN];
	pid_t child;
	int status = 0;
	int rc;

	if (!impl || !container_id || !image) {
		return -EINVAL;
	}

	rc = mkga_containerd_log_path(impl, container_id, log_path, sizeof(log_path));
	if (rc != 0) {
		return rc;
	}
	(void)unlink(log_path);

	child = fork();
	if (child < 0) {
		return -errno;
	}
	if (child == 0) {
		pid_t grandchild;

		grandchild = fork();
		if (grandchild < 0) {
			_exit(1);
		}
		if (grandchild > 0) {
			_exit(0);
		}

		(void)setsid();
		(void)mkga_containerd_run_logged_foreground(impl, container_id, image,
							   argv_count, argv_items, log_path);
		_exit(0);
	}

	while (waitpid(child, &status, 0) < 0) {
		if (errno != EINTR) {
			return -errno;
		}
	}
	if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
		return -EIO;
	}
	return 0;
}

static int mkga_containerd_spawn_exit_watcher(struct mkga_containerd_runtime *impl,
					      const char *container_id)
{
	pid_t child;
	int status = 0;

	if (!impl || !container_id || container_id[0] == '\0') {
		return -EINVAL;
	}

	child = fork();
	if (child < 0) {
		return -errno;
	}
	if (child == 0) {
		pid_t grandchild;

		grandchild = fork();
		if (grandchild < 0) {
			_exit(1);
		}
		if (grandchild > 0) {
			_exit(0);
		}

		(void)setsid();

		for (;;) {
			struct mkga_containerd_state current_state;
			int32_t exit_code = 0;
			int rc;

			rc = mkga_containerd_wait_for_exit(impl, container_id, INT32_MAX, &exit_code);
			if (rc == 0) {
				if (mkga_containerd_read_state(impl, container_id, &current_state) != 0) {
						mkga_containerd_fill_state(&current_state,
								   MKGA_CONTAINER_STATE_EXITED,
								   exit_code,
								   0,
								   0,
								   mkga_now_realtime_nanos(),
								   "");
					} else {
						current_state.state = MKGA_CONTAINER_STATE_EXITED;
					current_state.exit_code = exit_code;
					current_state.pid = 0;
					current_state.finished_at_unix_nano = mkga_now_realtime_nanos();
					current_state.message[0] = '\0';
				}
				(void)mkga_containerd_write_state(impl, container_id, &current_state);
				_exit(0);
			}
			if (rc == -ENOENT) {
				if (mkga_containerd_read_state(impl, container_id, &current_state) == 0 &&
				    current_state.state == MKGA_CONTAINER_STATE_EXITED) {
					_exit(0);
				}
			}
			usleep(200U * 1000U);
		}
	}

	while (waitpid(child, &status, 0) < 0) {
		if (errno != EINTR) {
			return -errno;
		}
	}
	if (!WIFEXITED(status) || WEXITSTATUS(status) != 0) {
		return -EIO;
	}
	return 0;
}

static int mkga_containerd_signal_task(struct mkga_containerd_runtime *impl,
				       const char *container_id,
				       const char *signal_name)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "tasks";
	argv[argc++] = "kill";
	argv[argc++] = "--all";
	argv[argc++] = "-s";
	argv[argc++] = (char *)signal_name;
	argv[argc++] = (char *)container_id;
	argv[argc] = NULL;

	return mkga_ctr_exec(impl, argv, impl->default_timeout_ms,
			     stdout_buf, sizeof(stdout_buf),
			     stderr_buf, sizeof(stderr_buf));
}

static int mkga_containerd_delete_task(struct mkga_containerd_runtime *impl,
				       const char *container_id)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;
	int rc;

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "tasks";
	argv[argc++] = "delete";
	argv[argc++] = (char *)container_id;
	argv[argc] = NULL;

	rc = mkga_ctr_exec(impl, argv, impl->default_timeout_ms,
			   stdout_buf, sizeof(stdout_buf),
			   stderr_buf, sizeof(stderr_buf));
	if (rc == -ENOENT) {
		return 0;
	}
	return rc;
}

static int mkga_containerd_delete_container(struct mkga_containerd_runtime *impl,
					    const char *container_id)
{
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;
	int rc;

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "containers";
	argv[argc++] = "delete";
	argv[argc++] = (char *)container_id;
	argv[argc] = NULL;

	rc = mkga_ctr_exec(impl, argv, impl->default_timeout_ms,
			   stdout_buf, sizeof(stdout_buf),
			   stderr_buf, sizeof(stderr_buf));
	if (rc == -ENOENT) {
		return 0;
	}
	return rc;
}

static int mkga_containerd_create_container(struct mkga_runtime *runtime,
					    const struct mkga_create_container_req *req,
					    struct mkga_create_container_resp *resp)
{
	struct mkga_containerd_runtime *impl;
	struct mkga_containerd_metadata meta;
	struct mkga_containerd_state state;
	char container_id[MKGA_MAX_ID_LEN];
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;
	int rc;

	if (!runtime || !req || !resp || !runtime->impl) {
		return -EINVAL;
	}
	impl = runtime->impl;

	rc = mkga_containerd_validate_create_req(req);
	if (rc != 0) {
		return rc;
	}

	rc = mkga_containerd_ensure_image(impl, req->image);
	if (rc != 0) {
		return rc;
	}

	rc = mkga_containerd_make_container_id(req, container_id, sizeof(container_id));
	if (rc != 0) {
		return rc;
	}

	memset(&meta, 0, sizeof(meta));
	mkga_copy_string(meta.image, sizeof(meta.image), req->image);
	meta.argv_count = req->argv_count;
	for (uint32_t i = 0; i < req->argv_count; i++) {
		mkga_copy_string(meta.argv[i], sizeof(meta.argv[i]), req->argv[i]);
	}
	mkga_containerd_fill_state(&state, MKGA_CONTAINER_STATE_CREATED, 0, 0, 0, 0, "");

	if (!impl->start_via_run) {
		memset(argv, 0, sizeof(argv));
		argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
		if (argc < 0) {
			return argc;
		}
		argv[argc++] = "containers";
		argv[argc++] = "create";
		argv[argc++] = (char *)req->image;
		argv[argc++] = container_id;
		argc = mkga_containerd_append_exec_argv(argv, argc, MKGA_CONTAINERD_ARGV_MAX,
					       req->argv_count,
					       meta.argv);
		if (argc < 0) {
			return argc;
		}
		argv[argc] = NULL;

		rc = mkga_ctr_exec(impl, argv, impl->default_timeout_ms,
				   stdout_buf, sizeof(stdout_buf),
				   stderr_buf, sizeof(stderr_buf));
		if (rc != 0) {
			return rc;
		}
	}

	rc = mkga_containerd_write_metadata(impl, container_id, &meta);
	if (rc != 0) {
		if (!impl->start_via_run) {
			(void)mkga_containerd_delete_container(impl, container_id);
		}
		return rc;
	}
	rc = mkga_containerd_write_state(impl, container_id, &state);
	if (rc != 0) {
		mkga_containerd_delete_metadata(impl, container_id);
		if (!impl->start_via_run) {
			(void)mkga_containerd_delete_container(impl, container_id);
		}
		return rc;
	}
	mkga_containerd_delete_log(impl, container_id);

	memset(resp, 0, sizeof(*resp));
	mkga_copy_string(resp->container_id, sizeof(resp->container_id), container_id);
	mkga_copy_string(resp->image_ref, sizeof(resp->image_ref), req->image);
	return 0;
}

static int mkga_containerd_start_container(struct mkga_runtime *runtime,
					   const struct mkga_container_control_req *req)
{
	struct mkga_containerd_runtime *impl;
	struct mkga_containerd_metadata meta;
	struct mkga_containerd_state state;
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char stdout_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	char stderr_buf[MKGA_CONTAINERD_OUTPUT_MAX];
	int argc;
	int rc;

	if (!runtime || !req || !runtime->impl) {
		return -EINVAL;
	}
	impl = runtime->impl;

	rc = mkga_containerd_validate_control_req(req);
	if (rc != 0) {
		return rc;
	}

	if (impl->start_via_run) {
		rc = mkga_containerd_read_metadata(impl, req->container_id, &meta);
		if (rc != 0) {
			return rc;
		}

		mkga_containerd_fill_state(&state, MKGA_CONTAINER_STATE_RUNNING, 0, 0,
					   mkga_now_realtime_nanos(), 0, "");
		rc = mkga_containerd_write_state(impl, req->container_id, &state);
		if (rc != 0) {
			return rc;
		}
		return mkga_containerd_spawn_log_runner(impl, req->container_id, meta.image,
							meta.argv_count, meta.argv);
	}

	memset(argv, 0, sizeof(argv));
	argc = mkga_ctr_fill_base_args(impl, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (argc < 0) {
		return argc;
	}
	argv[argc++] = "tasks";
	argv[argc++] = "start";
	argv[argc++] = "-d";
	argv[argc++] = (char *)req->container_id;
	argv[argc] = NULL;

	rc = mkga_ctr_exec(impl, argv, impl->default_timeout_ms,
			   stdout_buf, sizeof(stdout_buf),
			   stderr_buf, sizeof(stderr_buf));
	if (rc != 0) {
		return rc;
	}
	mkga_containerd_fill_state(&state, MKGA_CONTAINER_STATE_RUNNING, 0, 0,
				   mkga_now_realtime_nanos(), 0, "");
	rc = mkga_containerd_write_state(impl, req->container_id, &state);
	if (rc != 0) {
		return rc;
	}
	return mkga_containerd_spawn_exit_watcher(impl, req->container_id);
}

static int mkga_containerd_stop_container(struct mkga_runtime *runtime,
					  const struct mkga_container_control_req *req,
					  struct mkga_stop_container_resp *resp)
{
	struct mkga_containerd_runtime *impl;
	struct mkga_containerd_state state;
	int64_t deadline_ms;
	int64_t term_deadline_ms;
	int timeout_ms;
	int rc;
	int32_t exit_code = 0;

	if (!runtime || !req || !resp || !runtime->impl) {
		return -EINVAL;
	}
	impl = runtime->impl;

	rc = mkga_containerd_validate_control_req(req);
	if (rc != 0) {
		return rc;
	}

	timeout_ms = mkga_containerd_effective_timeout(
		impl, req->timeout_millis, impl->default_timeout_ms);
	deadline_ms = mkga_now_monotonic_millis() + timeout_ms;
	term_deadline_ms = deadline_ms;
	if (timeout_ms > MKGA_CONTAINERD_STOP_TERM_GRACE_MS) {
		term_deadline_ms = mkga_now_monotonic_millis() + MKGA_CONTAINERD_STOP_TERM_GRACE_MS;
	}

	rc = mkga_containerd_signal_task(impl, req->container_id, "SIGTERM");
	if (rc == -ENOENT) {
		resp->exit_code = 0;
		return 0;
	}
	if (rc != 0) {
		return rc;
	}

	if (impl->start_via_run) {
		rc = mkga_containerd_poll_for_exit_until(impl, req->container_id, term_deadline_ms,
							 &exit_code);
	} else {
		rc = mkga_containerd_wait_for_exit_until(impl, req->container_id, term_deadline_ms,
							 &exit_code);
	}
	if (rc == -ETIMEDOUT) {
		rc = mkga_containerd_signal_task(impl, req->container_id, "SIGKILL");
		if (rc != 0 && rc != -ENOENT) {
			return rc;
		}
		if (impl->start_via_run) {
			rc = mkga_containerd_poll_for_exit_until(impl, req->container_id, deadline_ms,
								 &exit_code);
		} else {
			rc = mkga_containerd_wait_for_exit_until(impl, req->container_id, deadline_ms,
								 &exit_code);
		}
	}
	if (rc != 0) {
		return rc;
	}

	resp->exit_code = exit_code;
	if (mkga_containerd_read_state(impl, req->container_id, &state) != 0) {
		mkga_containerd_fill_state(&state, MKGA_CONTAINER_STATE_EXITED, exit_code, 0,
					   0, mkga_now_realtime_nanos(), "");
	} else {
		state.state = MKGA_CONTAINER_STATE_EXITED;
		state.exit_code = exit_code;
		state.pid = 0;
		state.finished_at_unix_nano = mkga_now_realtime_nanos();
		state.message[0] = '\0';
	}
	(void)mkga_containerd_write_state(impl, req->container_id, &state);
	return 0;
}

static int mkga_containerd_remove_container(struct mkga_runtime *runtime,
					    const struct mkga_container_control_req *req)
{
	struct mkga_containerd_runtime *impl;
	int rc;

	if (!runtime || !req || !runtime->impl) {
		return -EINVAL;
	}
	impl = runtime->impl;

	rc = mkga_containerd_validate_control_req(req);
	if (rc != 0) {
		return rc;
	}

	rc = mkga_containerd_signal_task(impl, req->container_id, "SIGKILL");
	if (rc != 0 && rc != -ENOENT) {
		return rc;
	}

	rc = mkga_containerd_delete_task(impl, req->container_id);
	if (rc != 0) {
		return rc;
	}

	rc = mkga_containerd_delete_container(impl, req->container_id);
	if (rc != 0) {
		return rc;
	}

	mkga_containerd_delete_metadata(impl, req->container_id);
	mkga_containerd_delete_state(impl, req->container_id);
	mkga_containerd_delete_log(impl, req->container_id);
	return 0;
}

static int mkga_containerd_status_container(struct mkga_runtime *runtime,
					    const struct mkga_container_control_req *req,
					    struct mkga_container_status_resp *resp)
{
	struct mkga_containerd_runtime *impl;
	struct mkga_containerd_state state;
	enum mkga_containerd_task_status task_status = MKGA_CONTAINERD_TASK_STATUS_UNKNOWN;
	int32_t pid = 0;
	int rc;

	if (!runtime || !req || !resp || !runtime->impl) {
		return -EINVAL;
	}
	impl = runtime->impl;

	rc = mkga_containerd_validate_control_req(req);
	if (rc != 0) {
		return rc;
	}

	rc = mkga_containerd_read_state(impl, req->container_id, &state);
	if (rc != 0) {
		return rc;
	}

	if (state.state == MKGA_CONTAINER_STATE_RUNNING) {
		rc = mkga_containerd_query_task_status(impl, req->container_id, &task_status, &pid);
		if (rc == 0) {
			if (task_status == MKGA_CONTAINERD_TASK_STATUS_STOPPED) {
				state.state = MKGA_CONTAINER_STATE_EXITED;
				state.pid = 0;
				if (state.finished_at_unix_nano == 0) {
					state.finished_at_unix_nano = mkga_now_realtime_nanos();
				}
				state.message[0] = '\0';
				(void)mkga_containerd_write_state(impl, req->container_id, &state);
			} else if (task_status == MKGA_CONTAINERD_TASK_STATUS_MISSING &&
				   !impl->start_via_run) {
				state.state = MKGA_CONTAINER_STATE_EXITED;
				state.pid = 0;
				if (state.finished_at_unix_nano == 0) {
					state.finished_at_unix_nano = mkga_now_realtime_nanos();
				}
				state.message[0] = '\0';
				(void)mkga_containerd_write_state(impl, req->container_id, &state);
			} else if (task_status == MKGA_CONTAINERD_TASK_STATUS_RUNNING) {
				state.pid = pid;
				(void)mkga_containerd_write_state(impl, req->container_id, &state);
			}
		}
	}

	memset(resp, 0, sizeof(*resp));
	resp->state = state.state;
	resp->exit_code = state.exit_code;
	resp->pid = state.pid;
	resp->started_at_unix_nano = state.started_at_unix_nano;
	resp->finished_at_unix_nano = state.finished_at_unix_nano;
	mkga_copy_string(resp->message, sizeof(resp->message), state.message);
	return 0;
}

static int mkga_containerd_read_log(struct mkga_runtime *runtime,
				    const struct mkga_read_log_req *req,
				    struct mkga_read_log_resp *resp)
{
	struct mkga_containerd_runtime *impl;
	struct mkga_containerd_state state;
	char log_path[MKGA_MAX_LOG_PATH_LEN];
	struct stat st;
	uint64_t file_size = 0;
	uint64_t offset;
	size_t max_bytes;
	size_t to_read = 0;
	int fd = -1;
	int rc;

	if (!runtime || !req || !resp || !runtime->impl) {
		return -EINVAL;
	}
	impl = runtime->impl;

	rc = mkga_containerd_validate_read_log_req(req);
	if (rc != 0) {
		return rc;
	}

	memset(resp, 0, sizeof(*resp));
	offset = req->offset;
	resp->next_offset = offset;

	rc = mkga_containerd_read_state(impl, req->container_id, &state);
	if (rc != 0) {
		return rc;
	}

	rc = mkga_containerd_log_path(impl, req->container_id, log_path, sizeof(log_path));
	if (rc != 0) {
		return rc;
	}

	fd = open(log_path, O_RDONLY);
	if (fd < 0) {
		if (errno == ENOENT) {
			resp->eof = state.state == MKGA_CONTAINER_STATE_EXITED;
			return 0;
		}
		return -errno;
	}

	if (fstat(fd, &st) != 0) {
		rc = -errno;
		(void)close(fd);
		return rc;
	}
	file_size = (uint64_t)st.st_size;

	if (offset > file_size) {
		offset = file_size;
	}
	resp->next_offset = offset;

	max_bytes = req->max_bytes > 0 ? (size_t)req->max_bytes : MKGA_MAX_LOG_CHUNK;
	if (max_bytes > MKGA_MAX_LOG_CHUNK) {
		max_bytes = MKGA_MAX_LOG_CHUNK;
	}

	if (offset < file_size) {
		to_read = (size_t)(file_size - offset);
		if (to_read > max_bytes) {
			to_read = max_bytes;
		}
	}

	if (to_read > 0) {
		ssize_t nread = pread(fd, resp->data, to_read, (off_t)offset);

		if (nread < 0) {
			rc = -errno;
			(void)close(fd);
			return rc;
		}
		resp->data_len = (uint32_t)nread;
		resp->next_offset = offset + (uint64_t)nread;
	}

	if (close(fd) != 0) {
		return -errno;
	}

	resp->eof = (resp->next_offset >= file_size &&
		     state.state == MKGA_CONTAINER_STATE_EXITED);
	return 0;
}

static int mkga_containerd_exec_tty_prepare(struct mkga_runtime *runtime,
					    const struct mkga_exec_tty_prepare_req *req,
					    struct mkga_exec_tty_prepare_resp *resp)
{
	(void)runtime;
	return mkga_session_prepare(req, resp);
}

static int mkga_containerd_exec_tty_start(struct mkga_runtime *runtime,
					  const struct mkga_exec_session_control_req *req)
{
	struct mkga_containerd_runtime *impl;
	struct mkga_exec_session *session;
	struct mkga_exec_tty_waiter_arg *waiter = NULL;
	struct winsize ws = {
		.ws_row = 24,
		.ws_col = 80,
	};
	char *argv[MKGA_CONTAINERD_ARGV_MAX];
	char *slave_name = NULL;
	enum mkga_containerd_task_status task_status = MKGA_CONTAINERD_TASK_STATUS_UNKNOWN;
	int32_t pid = 0;
	pid_t child;
	int master_fd = -1;
	int rc;

	if (!runtime || !req || !runtime->impl) {
		return -EINVAL;
	}
	impl = runtime->impl;
	if (req->session_id[0] == '\0') {
		return -EINVAL;
	}

	rc = mkga_stream_ensure_started();
	if (rc != 0) {
		(void)fprintf(stderr,
			      "mkga exec_tty_start session=%s stream_start_rc=%d\n",
			      req->session_id,
			      rc);
		return rc;
	}

	session = mkga_session_lookup(req->session_id);
	if (!session) {
		(void)fprintf(stderr,
			      "mkga exec_tty_start session=%s missing_session\n",
			      req->session_id);
		return -ENOENT;
	}

	pthread_mutex_lock(&session->lock);
	if (session->state == MKGA_EXEC_SESSION_RUNNING) {
		pthread_mutex_unlock(&session->lock);
		return 0;
	}
	if (session->state != MKGA_EXEC_SESSION_PREPARED) {
		pthread_mutex_unlock(&session->lock);
		return -EINVAL;
	}
	if (session->exec_id[0] == '\0') {
		(void)snprintf(session->exec_id, sizeof(session->exec_id),
			       "%s", session->session_id);
	}
	pthread_mutex_unlock(&session->lock);

	rc = mkga_containerd_query_task_status(impl, session->container_id, &task_status, &pid);
	if (rc != 0) {
		(void)fprintf(stderr,
			      "mkga exec_tty_start session=%s container=%s status_query_rc=%d\n",
			      session->session_id,
			      session->container_id,
			      rc);
		return rc;
	}
	switch (task_status) {
	case MKGA_CONTAINERD_TASK_STATUS_RUNNING:
		(void)fprintf(stderr,
			      "mkga exec_tty_start session=%s container=%s task_running pid=%d\n",
			      session->session_id,
			      session->container_id,
			      pid);
		break;
	case MKGA_CONTAINERD_TASK_STATUS_STOPPED:
	case MKGA_CONTAINERD_TASK_STATUS_MISSING:
		(void)fprintf(stderr,
			      "mkga exec_tty_start session=%s container=%s task_not_running status=%d pid=%d\n",
			      session->session_id,
			      session->container_id,
			      (int)task_status,
			      pid);
		return -ENOENT;
	default:
		(void)fprintf(stderr,
			      "mkga exec_tty_start session=%s container=%s task_status_unknown status=%d pid=%d\n",
			      session->session_id,
			      session->container_id,
			      (int)task_status,
			      pid);
		return -EIO;
	}

	rc = mkga_containerd_build_exec_tty_argv(impl, session, argv, MKGA_CONTAINERD_ARGV_MAX);
	if (rc < 0) {
		return rc;
	}

	master_fd = posix_openpt(O_RDWR | O_NOCTTY);
	if (master_fd < 0) {
		return -errno;
	}
	if (grantpt(master_fd) != 0 || unlockpt(master_fd) != 0) {
		rc = -errno;
		(void)close(master_fd);
		return rc;
	}
	slave_name = ptsname(master_fd);
	if (!slave_name) {
		rc = -errno;
		(void)close(master_fd);
		return rc;
	}
	if (ioctl(master_fd, TIOCSWINSZ, &ws) != 0) {
		rc = -errno;
		(void)close(master_fd);
		return rc;
	}

	child = fork();
	if (child < 0) {
		rc = -errno;
		(void)close(master_fd);
		return rc;
	}
	if (child == 0) {
		int slave_fd;

		(void)setsid();
		slave_fd = open(slave_name, O_RDWR);
		if (slave_fd < 0) {
			_exit(errno == ENOENT ? 127 : 126);
		}
		(void)ioctl(slave_fd, TIOCSCTTY, 0);
		(void)dup2(slave_fd, STDIN_FILENO);
		(void)dup2(slave_fd, STDOUT_FILENO);
		(void)dup2(slave_fd, STDERR_FILENO);
		if (slave_fd > STDERR_FILENO) {
			(void)close(slave_fd);
		}
		(void)close(master_fd);
		(void)setenv("TERM", "xterm", 0);
		execvp(argv[0], argv);
		_exit(errno == ENOENT ? 127 : 126);
	}

	rc = mkga_set_nonblock(master_fd);
	if (rc != 0) {
		(void)kill(child, SIGKILL);
		(void)waitpid(child, NULL, 0);
		(void)close(master_fd);
		return rc;
	}

	waiter = calloc(1, sizeof(*waiter));
	if (!waiter) {
		(void)kill(child, SIGKILL);
		(void)waitpid(child, NULL, 0);
		(void)close(master_fd);
		return -ENOMEM;
	}
	waiter->session = session;

	pthread_mutex_lock(&session->lock);
	session->master_fd = master_fd;
	session->child_pid = child;
	session->exit_code = 0;
	session->state = MKGA_EXEC_SESSION_RUNNING;
	pthread_mutex_unlock(&session->lock);

	rc = pthread_create(&session->io_thread, NULL, mkga_containerd_exec_tty_output_pump, session);
	if (rc != 0) {
		pthread_mutex_lock(&session->lock);
		session->state = MKGA_EXEC_SESSION_PREPARED;
		session->master_fd = -1;
		session->child_pid = -1;
		pthread_mutex_unlock(&session->lock);
		free(waiter);
		(void)kill(child, SIGKILL);
		(void)waitpid(child, NULL, 0);
		(void)close(master_fd);
		return -rc;
	}
	(void)pthread_detach(session->io_thread);
	rc = pthread_create(&session->wait_thread, NULL, mkga_containerd_exec_tty_waiter, waiter);
	if (rc != 0) {
		pthread_mutex_lock(&session->lock);
		session->state = MKGA_EXEC_SESSION_PREPARED;
		session->master_fd = -1;
		session->child_pid = -1;
		pthread_mutex_unlock(&session->lock);
		free(waiter);
		(void)kill(child, SIGKILL);
		(void)waitpid(child, NULL, 0);
		(void)close(master_fd);
		return -rc;
	}
	(void)pthread_detach(session->wait_thread);
	return 0;
}

static int mkga_containerd_exec_tty_resize(struct mkga_runtime *runtime,
					   const struct mkga_exec_tty_resize_req *req)
{
	struct mkga_exec_session *session;
	struct winsize ws;
	int master_fd;

	(void)runtime;
	if (!req || req->session_id[0] == '\0' || req->width == 0 || req->height == 0) {
		return -EINVAL;
	}

	session = mkga_session_lookup(req->session_id);
	if (!session) {
		return -ENOENT;
	}

	pthread_mutex_lock(&session->lock);
	if (session->master_fd < 0 || session->state != MKGA_EXEC_SESSION_RUNNING) {
		pthread_mutex_unlock(&session->lock);
		return -EINVAL;
	}
	master_fd = session->master_fd;
	pthread_mutex_unlock(&session->lock);

	memset(&ws, 0, sizeof(ws));
	ws.ws_col = (unsigned short)req->width;
	ws.ws_row = (unsigned short)req->height;
	if (ioctl(master_fd, TIOCSWINSZ, &ws) != 0) {
		return -errno;
	}
	return 0;
}

static int mkga_containerd_exec_tty_close(struct mkga_runtime *runtime,
					  const struct mkga_exec_session_control_req *req)
{
	struct mkga_exec_session *session;
	pid_t child_pid = -1;

	(void)runtime;
	if (!req || req->session_id[0] == '\0') {
		return -EINVAL;
	}

	session = mkga_session_lookup(req->session_id);
	if (!session) {
		return -ENOENT;
	}

	pthread_mutex_lock(&session->lock);
	child_pid = session->child_pid;
	session->state = MKGA_EXEC_SESSION_CLOSED;
	if (session->master_fd >= 0) {
		(void)close(session->master_fd);
		session->master_fd = -1;
	}
	pthread_mutex_unlock(&session->lock);

	if (child_pid > 0) {
		if (kill(child_pid, SIGHUP) != 0 && errno != ESRCH) {
			return -errno;
		}
	}
	return 0;
}

static void mkga_containerd_destroy(struct mkga_runtime *runtime)
{
	if (!runtime) {
		return;
	}
	free(runtime->impl);
	free(runtime);
}

static const struct mkga_runtime_ops mkga_containerd_ops = {
	.create_container = mkga_containerd_create_container,
	.start_container = mkga_containerd_start_container,
	.stop_container = mkga_containerd_stop_container,
	.remove_container = mkga_containerd_remove_container,
	.status_container = mkga_containerd_status_container,
	.read_log = mkga_containerd_read_log,
	.exec_tty_prepare = mkga_containerd_exec_tty_prepare,
	.exec_tty_start = mkga_containerd_exec_tty_start,
	.exec_tty_resize = mkga_containerd_exec_tty_resize,
	.exec_tty_close = mkga_containerd_exec_tty_close,
	.destroy = mkga_containerd_destroy,
};

struct mkga_runtime *mkga_containerd_runtime_create(const char *socket_path)
{
	struct mkga_runtime *runtime;
	struct mkga_containerd_runtime *impl;
	int rc;

	runtime = calloc(1, sizeof(*runtime));
	impl = calloc(1, sizeof(*impl));
	if (!runtime || !impl) {
		free(runtime);
		free(impl);
		errno = ENOMEM;
		return NULL;
	}

	mkga_copy_string(impl->socket_path, sizeof(impl->socket_path),
			 socket_path ? socket_path : "/run/containerd/containerd.sock");
	mkga_copy_string(impl->ctr_path, sizeof(impl->ctr_path),
			 getenv("MK_GUEST_AGENT_CTR_PATH") ?
				 getenv("MK_GUEST_AGENT_CTR_PATH") :
				 MKGA_CONTAINERD_DEFAULT_CTR_PATH);
	mkga_copy_string(impl->runc_binary, sizeof(impl->runc_binary),
			 getenv("MK_GUEST_AGENT_RUNC_BINARY") ?
				 getenv("MK_GUEST_AGENT_RUNC_BINARY") :
				 MKGA_CONTAINERD_DEFAULT_RUNC_BINARY);
	mkga_copy_string(impl->state_root, sizeof(impl->state_root),
			 getenv("MK_GUEST_AGENT_CONTAINERD_STATE_ROOT") ?
				 getenv("MK_GUEST_AGENT_CONTAINERD_STATE_ROOT") :
				 MKGA_CONTAINERD_DEFAULT_STATE_ROOT);
	mkga_copy_string(impl->namespace_name, sizeof(impl->namespace_name),
			 getenv("MK_GUEST_AGENT_CONTAINERD_NAMESPACE") ?
				 getenv("MK_GUEST_AGENT_CONTAINERD_NAMESPACE") :
				 MKGA_CONTAINERD_DEFAULT_NAMESPACE);
	impl->default_timeout_ms = mkga_getenv_int(
		"MK_GUEST_AGENT_CONTAINERD_TIMEOUT_MS",
		MKGA_CONTAINERD_DEFAULT_TIMEOUT_MS);
	impl->no_pivot = mkga_getenv_bool("MK_GUEST_AGENT_CONTAINERD_NO_PIVOT", false);
	impl->start_via_run = mkga_getenv_bool("MK_GUEST_AGENT_CONTAINERD_START_VIA_RUN",
						       impl->no_pivot);
	impl->null_io = mkga_getenv_bool("MK_GUEST_AGENT_CONTAINERD_NULL_IO", false);

	rc = mkga_containerd_wait_ready(impl, impl->default_timeout_ms);
	if (rc != 0) {
		errno = rc < 0 ? -rc : EIO;
		free(runtime);
		free(impl);
		return NULL;
	}

	runtime->ops = &mkga_containerd_ops;
	runtime->impl = impl;
	return runtime;
}
