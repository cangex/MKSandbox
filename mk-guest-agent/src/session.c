#include "session.h"

#include <errno.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

static pthread_mutex_t mkga_session_mu = PTHREAD_MUTEX_INITIALIZER;
static struct mkga_exec_session *mkga_sessions;

static void mkga_session_copy_string(char *dst, size_t dst_len, const char *src)
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

struct mkga_exec_session *mkga_session_lookup(const char *session_id)
{
	struct mkga_exec_session *current;

	pthread_mutex_lock(&mkga_session_mu);
	current = mkga_sessions;
	while (current) {
		if (session_id && strcmp(current->session_id, session_id) == 0) {
			pthread_mutex_unlock(&mkga_session_mu);
			return current;
		}
		current = current->next;
	}
	pthread_mutex_unlock(&mkga_session_mu);
	return NULL;
}

static struct mkga_exec_session *mkga_session_find_locked(const char *session_id)
{
	struct mkga_exec_session *current = mkga_sessions;

	while (current) {
		if (session_id && strcmp(current->session_id, session_id) == 0) {
			return current;
		}
		current = current->next;
	}
	return NULL;
}

int mkga_session_prepare(const struct mkga_exec_tty_prepare_req *req,
			 struct mkga_exec_tty_prepare_resp *resp)
{
	struct mkga_exec_session *session;

	if (!req || !resp) {
		return -EINVAL;
	}
	if (req->argv_count == 0) {
		return -EINVAL;
	}
	if (req->argv_count > MKGA_MAX_ARGV) {
		return -EINVAL;
	}
	if (req->container_id[0] == '\0') {
		return -EINVAL;
	}

	session = calloc(1, sizeof(*session));
	if (!session) {
		return -ENOMEM;
	}
	if (pthread_mutex_init(&session->lock, NULL) != 0) {
		free(session);
		return -ENOMEM;
	}

	session->tty = req->tty != 0;
	session->stdin_enabled = req->stdin_enabled != 0;
	session->stdout_enabled = req->stdout_enabled != 0;
	session->stderr_enabled = req->stderr_enabled != 0;
	session->argv_count = req->argv_count;
	session->state = MKGA_EXEC_SESSION_PREPARED;
	session->master_fd = -1;
	session->child_pid = -1;
	mkga_session_copy_string(session->container_id, sizeof(session->container_id),
				 req->container_id);
	for (uint32_t i = 0; i < req->argv_count; i++) {
		mkga_session_copy_string(session->argv[i], sizeof(session->argv[i]),
					 req->argv[i]);
	}
	(void)snprintf(session->session_id, sizeof(session->session_id), "exec-%lld",
		       (long long)mkga_now_millis());
	mkga_session_copy_string(resp->session_id, sizeof(resp->session_id),
				 session->session_id);
	(void)fprintf(stderr,
		      "mkga exec_tty_prepare session=%s container=%s argc=%u\n",
		      session->session_id,
		      session->container_id,
		      session->argv_count);

	pthread_mutex_lock(&mkga_session_mu);
	session->next = mkga_sessions;
	mkga_sessions = session;
	pthread_mutex_unlock(&mkga_session_mu);
	return 0;
}

int mkga_session_start(const struct mkga_exec_session_control_req *req)
{
	struct mkga_exec_session *session;

	if (!req || req->session_id[0] == '\0') {
		return -EINVAL;
	}

	pthread_mutex_lock(&mkga_session_mu);
	session = mkga_session_find_locked(req->session_id);
	if (!session) {
		pthread_mutex_unlock(&mkga_session_mu);
		return -ENOENT;
	}
	pthread_mutex_lock(&session->lock);
	pthread_mutex_unlock(&mkga_session_mu);

	switch (session->state) {
	case MKGA_EXEC_SESSION_PREPARED:
		session->state = MKGA_EXEC_SESSION_RUNNING;
		if (session->exec_id[0] == '\0') {
			(void)snprintf(session->exec_id, sizeof(session->exec_id),
				       "%s", session->session_id);
		}
		pthread_mutex_unlock(&session->lock);
		return 0;
	case MKGA_EXEC_SESSION_RUNNING:
		pthread_mutex_unlock(&session->lock);
		return 0;
	case MKGA_EXEC_SESSION_EXITED:
	case MKGA_EXEC_SESSION_CLOSED:
		pthread_mutex_unlock(&session->lock);
		return -EINVAL;
	default:
		pthread_mutex_unlock(&session->lock);
		return -EIO;
	}
}

int mkga_session_resize(const struct mkga_exec_tty_resize_req *req)
{
	(void)req;
	return -ENOSYS;
}

int mkga_session_close(const struct mkga_exec_session_control_req *req)
{
	(void)req;
	return -ENOSYS;
}

int mkga_session_write_stdin(const char *session_id, const uint8_t *data, size_t len)
{
	struct mkga_exec_session *session;
	size_t written = 0;
	int master_fd;

	if (!session_id || !data || len == 0) {
		return -EINVAL;
	}

	session = mkga_session_lookup(session_id);
	if (!session) {
		return -ENOENT;
	}

	pthread_mutex_lock(&session->lock);
	if (session->master_fd < 0 || session->state != MKGA_EXEC_SESSION_RUNNING ||
	    !session->stdin_enabled) {
		pthread_mutex_unlock(&session->lock);
		return -EINVAL;
	}
	master_fd = session->master_fd;
	pthread_mutex_unlock(&session->lock);
	(void)fprintf(stderr,
		      "mkga session write stdin session=%s bytes=%zu master_fd=%d\n",
		      session_id,
		      len,
		      master_fd);

	while (written < len) {
		ssize_t rc = write(master_fd, data + written, len - written);

		if (rc > 0) {
			written += (size_t)rc;
			continue;
		}
		if (rc < 0 && errno == EINTR) {
			continue;
		}
		if (rc < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
			struct pollfd pfd;

			memset(&pfd, 0, sizeof(pfd));
			pfd.fd = master_fd;
			pfd.events = POLLOUT;
			if (poll(&pfd, 1, 1000) < 0 && errno != EINTR) {
				return -errno;
			}
			continue;
		}
		return rc < 0 ? -errno : -EIO;
	}

	return 0;
}
