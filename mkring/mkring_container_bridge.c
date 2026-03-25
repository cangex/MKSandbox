#include <linux/atomic.h>
#include <linux/completion.h>
#include <linux/errno.h>
#include <linux/fs.h>
#include <linux/jiffies.h>
#include <linux/kernel.h>
#include <linux/kthread.h>
#include <linux/list.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/mutex.h>
#include <linux/poll.h>
#include <linux/slab.h>
#include <linux/spinlock.h>
#include <linux/string.h>
#include <linux/uaccess.h>
#include <linux/wait.h>

#include "mkring.h"
#include "mkring_demux.h"
#include "mkring_proto.h"
#include "mkring_container.h"

#define MKRC_NAME			"mkring-container-bridge"
#define MKRC_DEFAULT_ROLE		"host"
#define MKRC_DEFAULT_DEVNODE		"mkring_container_bridge"
#define MKRC_DEFAULT_TIMEOUT_MS		5000U

enum mkrc_role {
	MKRC_ROLE_INVALID = 0,
	MKRC_ROLE_HOST = 1,
	MKRC_ROLE_GUEST = 2,
};

struct mkrc_msg_node {
	struct list_head node;
	struct mkring_container_packet packet;
};

struct mkrc_pending_call {
	struct list_head node;
	struct completion done;
	u16 peer_kernel_id;
	u64 request_id;
	struct mkring_container_message response;
};

struct mkrc_ctx {
	enum mkrc_role role;
	char device_name[64];
	struct mkring_info info;
	bool peer_ready[MKRING_MAX_KERNELS];
	bool stopping;

	struct miscdevice miscdev;

	spinlock_t work_lock;
	struct list_head work_queue;
	wait_queue_head_t work_wq;
	atomic_t work_pending;

	spinlock_t guest_lock;
	struct list_head guest_queue;
	wait_queue_head_t guest_wq;
	atomic_t guest_pending;

	spinlock_t pending_lock;
	struct list_head pending_calls;

	wait_queue_head_t ready_wq;
	struct task_struct *worker;

	atomic64_t next_request_id;
	atomic64_t rx_ok;
	atomic64_t rx_bad;
	atomic64_t tx_ok;
	atomic64_t tx_fail;
};

static char *role = MKRC_DEFAULT_ROLE;
module_param(role, charp, 0444);
MODULE_PARM_DESC(role, "Bridge role: host or guest");

static char *device_name = MKRC_DEFAULT_DEVNODE;
module_param(device_name, charp, 0444);
MODULE_PARM_DESC(device_name, "Misc device node name");

static unsigned int default_timeout_ms = MKRC_DEFAULT_TIMEOUT_MS;
module_param(default_timeout_ms, uint, 0644);
MODULE_PARM_DESC(default_timeout_ms, "Default timeout in milliseconds for host CALL");

static struct mkrc_ctx mkrc;

static enum mkrc_role mkrc_parse_role(const char *value)
{
	if (!value)
		return MKRC_ROLE_INVALID;
	if (!strcmp(value, "host"))
		return MKRC_ROLE_HOST;
	if (!strcmp(value, "guest"))
		return MKRC_ROLE_GUEST;
	return MKRC_ROLE_INVALID;
}

static bool mkrc_valid_peer(const struct mkrc_ctx *ctx, u16 peer_kernel_id)
{
	return peer_kernel_id < ctx->info.kernels &&
	       peer_kernel_id != ctx->info.local_id;
}

static bool mkrc_valid_operation(u8 operation)
{
	switch (operation) {
	case MKRING_CONTAINER_OP_CREATE:
	case MKRING_CONTAINER_OP_START:
	case MKRING_CONTAINER_OP_STOP:
	case MKRING_CONTAINER_OP_REMOVE:
	case MKRING_CONTAINER_OP_STATUS:
	case MKRING_CONTAINER_OP_READ_LOG:
	case MKRING_CONTAINER_OP_EXEC_TTY_PREPARE:
	case MKRING_CONTAINER_OP_EXEC_TTY_START:
	case MKRING_CONTAINER_OP_EXEC_TTY_RESIZE:
	case MKRING_CONTAINER_OP_EXEC_TTY_CLOSE:
		return true;
	default:
		return false;
	}
}

static u32 mkrc_payload_len_for_message(const struct mkring_container_message *msg)
{
	switch (msg->hdr.kind) {
	case MKRING_CONTAINER_KIND_READY:
		return sizeof(msg->payload.ready);
	case MKRING_CONTAINER_KIND_REQUEST:
		switch (msg->hdr.operation) {
		case MKRING_CONTAINER_OP_CREATE:
			return sizeof(msg->payload.create_req);
		case MKRING_CONTAINER_OP_START:
		case MKRING_CONTAINER_OP_STOP:
		case MKRING_CONTAINER_OP_REMOVE:
		case MKRING_CONTAINER_OP_STATUS:
			return sizeof(msg->payload.control_req);
		case MKRING_CONTAINER_OP_READ_LOG:
			return sizeof(msg->payload.read_log_req);
		case MKRING_CONTAINER_OP_EXEC_TTY_PREPARE:
			return sizeof(msg->payload.exec_tty_prepare_req);
		case MKRING_CONTAINER_OP_EXEC_TTY_START:
			return sizeof(msg->payload.exec_tty_start_req);
		case MKRING_CONTAINER_OP_EXEC_TTY_RESIZE:
			return sizeof(msg->payload.exec_tty_resize_req);
		case MKRING_CONTAINER_OP_EXEC_TTY_CLOSE:
			return sizeof(msg->payload.exec_tty_close_req);
		default:
			return 0;
		}
	case MKRING_CONTAINER_KIND_RESPONSE:
		if (msg->hdr.status != 0)
			return sizeof(msg->payload.error);
		switch (msg->hdr.operation) {
		case MKRING_CONTAINER_OP_CREATE:
			return sizeof(msg->payload.create_resp);
		case MKRING_CONTAINER_OP_STOP:
			return sizeof(msg->payload.stop_resp);
		case MKRING_CONTAINER_OP_STATUS:
			return sizeof(msg->payload.status_resp);
		case MKRING_CONTAINER_OP_READ_LOG:
			return sizeof(msg->payload.read_log_resp);
		case MKRING_CONTAINER_OP_EXEC_TTY_PREPARE:
			return sizeof(msg->payload.exec_tty_prepare_resp);
		case MKRING_CONTAINER_OP_EXEC_TTY_START:
		case MKRING_CONTAINER_OP_EXEC_TTY_RESIZE:
		case MKRING_CONTAINER_OP_EXEC_TTY_CLOSE:
			return 0;
		case MKRING_CONTAINER_OP_START:
		case MKRING_CONTAINER_OP_REMOVE:
			return 0;
		default:
			return 0;
		}
	default:
		return 0;
	}
}

static void mkrc_prepare_message(struct mkring_container_message *msg,
				 u8 kind, u8 operation, u64 request_id)
{
	memset(msg, 0, sizeof(*msg));
	msg->hdr.magic = MKRING_CONTAINER_MAGIC;
	msg->hdr.version = MKRING_CONTAINER_VERSION;
	msg->hdr.channel = MKRING_CONTAINER_CHANNEL;
	msg->hdr.kind = kind;
	msg->hdr.operation = operation;
	msg->hdr.request_id = request_id;
	msg->hdr.status = 0;
	msg->hdr.payload_len = mkrc_payload_len_for_message(msg);
}

static bool mkrc_message_has_valid_identity(const struct mkring_container_message *msg)
{
	if (!msg)
		return false;
	if (msg->hdr.magic != MKRING_CONTAINER_MAGIC)
		return false;
	if (msg->hdr.version != MKRING_CONTAINER_VERSION)
		return false;
	if (msg->hdr.channel != MKRING_CONTAINER_CHANNEL)
		return false;

	switch (msg->hdr.kind) {
	case MKRING_CONTAINER_KIND_READY:
		return msg->hdr.operation == MKRING_CONTAINER_OP_NONE;
	case MKRING_CONTAINER_KIND_REQUEST:
	case MKRING_CONTAINER_KIND_RESPONSE:
		return mkrc_valid_operation(msg->hdr.operation);
	default:
		return false;
	}
}

static bool mkrc_message_is_valid(const struct mkring_container_message *msg,
				  u32 len)
{
	if (!mkrc_message_has_valid_identity(msg))
		return false;
	if (len != sizeof(*msg))
		return false;
	return msg->hdr.payload_len == mkrc_payload_len_for_message(msg);
}

static bool mkrc_demux_validate(const void *data, u32 len, void *priv)
{
	const struct mkrc_ctx *ctx = priv;
	const struct mkring_container_message *msg = data;

	if (!ctx || !msg)
		return false;
	if (len != sizeof(*msg))
		return false;

	return mkrc_message_is_valid(msg, len);
}

static int mkrc_send_packet(struct mkrc_ctx *ctx, u16 peer_kernel_id,
			    struct mkring_container_message *msg)
{
	int ret;

	if (!mkrc_valid_peer(ctx, peer_kernel_id))
		return -EINVAL;
	if (!mkrc_message_has_valid_identity(msg))
		return -EINVAL;

	msg->hdr.payload_len = mkrc_payload_len_for_message(msg);
	ret = mkring_send(peer_kernel_id, msg, sizeof(*msg));
	if (ret) {
		atomic64_inc(&ctx->tx_fail);
		return ret;
	}

	atomic64_inc(&ctx->tx_ok);
	return 0;
}

static struct mkrc_msg_node *mkrc_pop_work(struct mkrc_ctx *ctx)
{
	struct mkrc_msg_node *node = NULL;
	unsigned long flags;

	spin_lock_irqsave(&ctx->work_lock, flags);
	if (!list_empty(&ctx->work_queue)) {
		node = list_first_entry(&ctx->work_queue, struct mkrc_msg_node, node);
		list_del(&node->node);
		atomic_dec(&ctx->work_pending);
	}
	spin_unlock_irqrestore(&ctx->work_lock, flags);
	return node;
}

static void mkrc_queue_guest_request(struct mkrc_ctx *ctx, struct mkrc_msg_node *node)
{
	unsigned long flags;

	spin_lock_irqsave(&ctx->guest_lock, flags);
	list_add_tail(&node->node, &ctx->guest_queue);
	atomic_inc(&ctx->guest_pending);
	spin_unlock_irqrestore(&ctx->guest_lock, flags);
	wake_up_interruptible(&ctx->guest_wq);
}

static void mkrc_complete_pending(struct mkrc_ctx *ctx,
				  const struct mkrc_msg_node *node)
{
	struct mkrc_pending_call *pending;
	struct mkrc_pending_call *matched = NULL;
	unsigned long flags;

	spin_lock_irqsave(&ctx->pending_lock, flags);
	list_for_each_entry(pending, &ctx->pending_calls, node) {
		if (pending->peer_kernel_id == node->packet.peer_kernel_id &&
		    pending->request_id == node->packet.msg.hdr.request_id) {
			pending->response = node->packet.msg;
			matched = pending;
			break;
		}
	}
	spin_unlock_irqrestore(&ctx->pending_lock, flags);

	if (!matched) {
		pr_warn("%s: unexpected response peer=%u request_id=%llu\n",
			MKRC_NAME, node->packet.peer_kernel_id,
			(unsigned long long)node->packet.msg.hdr.request_id);
		return;
	}

	complete(&matched->done);
}

static void mkrc_handle_ready(struct mkrc_ctx *ctx,
			      const struct mkrc_msg_node *node)
{
	if (ctx->role != MKRC_ROLE_HOST) {
		pr_warn("%s: dropping READY in role=%d from peer=%u\n",
			MKRC_NAME, ctx->role, node->packet.peer_kernel_id);
		return;
	}

	ctx->peer_ready[node->packet.peer_kernel_id] = true;
	wake_up_interruptible(&ctx->ready_wq);

	pr_info("%s: peer=%u runtime=%s features=0x%x ready\n",
		MKRC_NAME, node->packet.peer_kernel_id,
		node->packet.msg.payload.ready.runtime_name,
		node->packet.msg.payload.ready.features);
}

static void mkrc_handle_request(struct mkrc_ctx *ctx, struct mkrc_msg_node *node)
{
	if (ctx->role != MKRC_ROLE_GUEST) {
		pr_warn("%s: dropping REQUEST in role=%d from peer=%u\n",
			MKRC_NAME, ctx->role, node->packet.peer_kernel_id);
		kfree(node);
		return;
	}

	mkrc_queue_guest_request(ctx, node);
}

static void mkrc_handle_response(struct mkrc_ctx *ctx,
				 const struct mkrc_msg_node *node)
{
	if (ctx->role != MKRC_ROLE_HOST) {
		pr_warn("%s: dropping RESPONSE in role=%d from peer=%u\n",
			MKRC_NAME, ctx->role, node->packet.peer_kernel_id);
		return;
	}

	mkrc_complete_pending(ctx, node);
}

static int mkrc_worker_thread(void *arg)
{
	struct mkrc_ctx *ctx = arg;

	while (!kthread_should_stop()) {
		struct mkrc_msg_node *node;

		wait_event_interruptible(ctx->work_wq,
					 ctx->stopping ||
						 atomic_read(&ctx->work_pending) > 0 ||
						 kthread_should_stop());
		if (ctx->stopping || kthread_should_stop())
			break;

		node = mkrc_pop_work(ctx);
		if (!node)
			continue;

		switch (node->packet.msg.hdr.kind) {
		case MKRING_CONTAINER_KIND_READY:
			mkrc_handle_ready(ctx, node);
			kfree(node);
			break;
		case MKRING_CONTAINER_KIND_REQUEST:
			mkrc_handle_request(ctx, node);
			break;
		case MKRING_CONTAINER_KIND_RESPONSE:
			mkrc_handle_response(ctx, node);
			kfree(node);
			break;
		default:
			kfree(node);
			break;
		}
	}

	return 0;
}

static void mkrc_demux_rx(u16 src_kid, const void *data, u32 len, void *priv)
{
	struct mkrc_ctx *ctx = priv;
	struct mkrc_msg_node *node;
	unsigned long flags;

	if (!ctx || !mkrc_valid_peer(ctx, src_kid) || !data) {
		if (ctx)
			atomic64_inc(&ctx->rx_bad);
		return;
	}

	node = kmalloc(sizeof(*node), GFP_ATOMIC);
	if (!node) {
		atomic64_inc(&ctx->rx_bad);
		pr_warn_ratelimited("%s: drop message from peer=%u due to OOM\n",
				    MKRC_NAME, src_kid);
		return;
	}

	memset(node, 0, sizeof(*node));
	INIT_LIST_HEAD(&node->node);
	node->packet.peer_kernel_id = src_kid;
	memcpy(&node->packet.msg, data, sizeof(node->packet.msg));

	spin_lock_irqsave(&ctx->work_lock, flags);
	list_add_tail(&node->node, &ctx->work_queue);
	atomic_inc(&ctx->work_pending);
	spin_unlock_irqrestore(&ctx->work_lock, flags);

	atomic64_inc(&ctx->rx_ok);
	wake_up_interruptible(&ctx->work_wq);
}

static const struct mkring_demux_ops mkrc_demux_ops = {
	.validate = mkrc_demux_validate,
	.rx = mkrc_demux_rx,
};

static struct mkrc_msg_node *mkrc_pop_guest_request(struct mkrc_ctx *ctx)
{
	struct mkrc_msg_node *node = NULL;
	unsigned long flags;

	spin_lock_irqsave(&ctx->guest_lock, flags);
	if (!list_empty(&ctx->guest_queue)) {
		node = list_first_entry(&ctx->guest_queue, struct mkrc_msg_node, node);
		list_del(&node->node);
		atomic_dec(&ctx->guest_pending);
	}
	spin_unlock_irqrestore(&ctx->guest_lock, flags);
	return node;
}

static long mkrc_wait_peer_ready(struct mkrc_ctx *ctx, u16 peer_kernel_id,
				 u32 timeout_ms)
{
	long wret;

	if (!mkrc_valid_peer(ctx, peer_kernel_id))
		return -EINVAL;
	if (ctx->role != MKRC_ROLE_HOST)
		return -EINVAL;
	if (ctx->peer_ready[peer_kernel_id])
		return 0;

	wret = wait_event_interruptible_timeout(
		ctx->ready_wq,
		ctx->peer_ready[peer_kernel_id] || ctx->stopping,
		msecs_to_jiffies(timeout_ms ? timeout_ms : default_timeout_ms));
	if (wret < 0)
		return wret;
	if (wret == 0)
		return -ETIMEDOUT;
	if (ctx->stopping)
		return -ESHUTDOWN;
	return 0;
}

static int mkrc_guest_read(struct mkrc_ctx *ctx, char __user *buf, size_t count,
			   int nonblock)
{
	struct mkrc_msg_node *node;
	long wret;

	if (count < sizeof(struct mkring_container_packet))
		return -EINVAL;
	if (ctx->role != MKRC_ROLE_GUEST)
		return -EINVAL;

	if (nonblock && atomic_read(&ctx->guest_pending) == 0)
		return -EAGAIN;

	wret = wait_event_interruptible(
		ctx->guest_wq,
		atomic_read(&ctx->guest_pending) > 0 || ctx->stopping);
	if (wret < 0)
		return (int)wret;
	if (ctx->stopping)
		return -ESHUTDOWN;

	node = mkrc_pop_guest_request(ctx);
	if (!node)
		return -EAGAIN;

	if (copy_to_user(buf, &node->packet, sizeof(node->packet))) {
		kfree(node);
		return -EFAULT;
	}

	kfree(node);
	return sizeof(struct mkring_container_packet);
}

static ssize_t mkrc_read(struct file *file, char __user *buf,
			 size_t count, loff_t *ppos)
{
	struct mkrc_ctx *ctx = file->private_data;

	(void)ppos;
	if (!ctx)
		return -ENODEV;

	return mkrc_guest_read(ctx, buf, count, file->f_flags & O_NONBLOCK);
}

static int mkrc_guest_write_response(struct mkrc_ctx *ctx,
				     const char __user *buf, size_t count)
{
	struct mkring_container_packet packet;
	int ret;

	if (count != sizeof(packet))
		return -EINVAL;
	if (ctx->role != MKRC_ROLE_GUEST)
		return -EINVAL;
	if (copy_from_user(&packet, buf, sizeof(packet)))
		return -EFAULT;
	if (!mkrc_valid_peer(ctx, packet.peer_kernel_id))
		return -EINVAL;
	if (packet.msg.hdr.kind != MKRING_CONTAINER_KIND_RESPONSE)
		return -EINVAL;
	if (!mkrc_valid_operation(packet.msg.hdr.operation))
		return -EINVAL;
	if (!packet.msg.hdr.request_id)
		return -EINVAL;

	ret = mkrc_send_packet(ctx, packet.peer_kernel_id, &packet.msg);
	if (ret)
		return ret;

	return sizeof(packet);
}

static ssize_t mkrc_write(struct file *file, const char __user *buf,
			  size_t count, loff_t *ppos)
{
	struct mkrc_ctx *ctx = file->private_data;

	(void)ppos;
	if (!ctx)
		return -ENODEV;

	return mkrc_guest_write_response(ctx, buf, count);
}

static long mkrc_ioctl_wait_ready(struct mkrc_ctx *ctx, unsigned long arg)
{
	struct mkring_container_wait_ready req;
	long ret;

	if (copy_from_user(&req, (void __user *)arg, sizeof(req)))
		return -EFAULT;

	ret = mkrc_wait_peer_ready(ctx, req.peer_kernel_id, req.timeout_ms);
	req.ready = ret == 0 ? 1 : 0;

	if (copy_to_user((void __user *)arg, &req, sizeof(req)))
		return -EFAULT;
	return ret;
}

static long mkrc_ioctl_set_ready(struct mkrc_ctx *ctx, unsigned long arg)
{
	struct mkring_container_set_ready req;
	struct mkring_container_message msg;

	if (ctx->role != MKRC_ROLE_GUEST)
		return -EINVAL;
	if (copy_from_user(&req, (void __user *)arg, sizeof(req)))
		return -EFAULT;
	if (!mkrc_valid_peer(ctx, req.peer_kernel_id))
		return -EINVAL;

	mkrc_prepare_message(&msg, MKRING_CONTAINER_KIND_READY,
			     MKRING_CONTAINER_OP_NONE, 0);
	msg.payload.ready.features = req.features;
	strscpy(msg.payload.ready.runtime_name, req.runtime_name,
		sizeof(msg.payload.ready.runtime_name));

	return mkrc_send_packet(ctx, req.peer_kernel_id, &msg);
}

static long mkrc_ioctl_call(struct mkrc_ctx *ctx, unsigned long arg)
{
	struct mkring_container_call call;
	struct mkrc_pending_call *pending;
	unsigned long flags;
	long wret;
	int ret;

	if (ctx->role != MKRC_ROLE_HOST)
		return -EINVAL;
	if (copy_from_user(&call, (void __user *)arg, sizeof(call)))
		return -EFAULT;
	if (!mkrc_valid_peer(ctx, call.peer_kernel_id))
		return -EINVAL;
	if (call.request.hdr.kind != MKRING_CONTAINER_KIND_REQUEST)
		return -EINVAL;
	if (!mkrc_valid_operation(call.request.hdr.operation))
		return -EINVAL;
	if (!ctx->peer_ready[call.peer_kernel_id])
		return -ENOLINK;

	if (!call.request.hdr.request_id)
		call.request.hdr.request_id =
			(u64)atomic64_inc_return(&ctx->next_request_id);
	call.request.hdr.magic = MKRING_CONTAINER_MAGIC;
	call.request.hdr.version = MKRING_CONTAINER_VERSION;
	call.request.hdr.channel = MKRING_CONTAINER_CHANNEL;
	call.request.hdr.kind = MKRING_CONTAINER_KIND_REQUEST;
	call.request.hdr.payload_len = mkrc_payload_len_for_message(&call.request);
	call.request.hdr.status = 0;
	memset(&call.response, 0, sizeof(call.response));
	call.status = 0;

	pending = kzalloc(sizeof(*pending), GFP_KERNEL);
	if (!pending)
		return -ENOMEM;

	init_completion(&pending->done);
	INIT_LIST_HEAD(&pending->node);
	pending->peer_kernel_id = call.peer_kernel_id;
	pending->request_id = call.request.hdr.request_id;

	spin_lock_irqsave(&ctx->pending_lock, flags);
	list_add_tail(&pending->node, &ctx->pending_calls);
	spin_unlock_irqrestore(&ctx->pending_lock, flags);

	ret = mkrc_send_packet(ctx, call.peer_kernel_id, &call.request);
	if (ret)
		goto out_remove_pending;

	wret = wait_for_completion_interruptible_timeout(
		&pending->done,
		msecs_to_jiffies(call.timeout_ms ? call.timeout_ms :
					 default_timeout_ms));
	if (wret < 0) {
		ret = (int)wret;
		goto out_remove_pending;
	}
	if (wret == 0) {
		ret = -ETIMEDOUT;
		goto out_remove_pending;
	}

	call.response = pending->response;
	call.status = pending->response.hdr.status;

	spin_lock_irqsave(&ctx->pending_lock, flags);
	list_del_init(&pending->node);
	spin_unlock_irqrestore(&ctx->pending_lock, flags);
	kfree(pending);

	if (copy_to_user((void __user *)arg, &call, sizeof(call)))
		return -EFAULT;
	return 0;

out_remove_pending:
	spin_lock_irqsave(&ctx->pending_lock, flags);
	if (!list_empty(&pending->node))
		list_del_init(&pending->node);
	spin_unlock_irqrestore(&ctx->pending_lock, flags);
	kfree(pending);
	return ret;
}

static long mkrc_unlocked_ioctl(struct file *file, unsigned int cmd,
				unsigned long arg)
{
	struct mkrc_ctx *ctx = file->private_data;

	if (!ctx)
		return -ENODEV;

	switch (cmd) {
	case MKRING_CONTAINER_IOC_WAIT_READY:
		return mkrc_ioctl_wait_ready(ctx, arg);
	case MKRING_CONTAINER_IOC_SET_READY:
		return mkrc_ioctl_set_ready(ctx, arg);
	case MKRING_CONTAINER_IOC_CALL:
		return mkrc_ioctl_call(ctx, arg);
	default:
		return -ENOTTY;
	}
}

static __poll_t mkrc_poll(struct file *file, poll_table *wait)
{
	struct mkrc_ctx *ctx = file->private_data;
	__poll_t mask = 0;

	if (!ctx)
		return EPOLLERR;

	if (ctx->role == MKRC_ROLE_GUEST) {
		poll_wait(file, &ctx->guest_wq, wait);
		if (atomic_read(&ctx->guest_pending) > 0)
			mask |= EPOLLIN | EPOLLRDNORM;
	}

	return mask;
}

static int mkrc_open(struct inode *inode, struct file *file)
{
	(void)inode;
	file->private_data = &mkrc;
	return 0;
}

static const struct file_operations mkrc_fops = {
	.owner = THIS_MODULE,
	.open = mkrc_open,
	.read = mkrc_read,
	.write = mkrc_write,
	.unlocked_ioctl = mkrc_unlocked_ioctl,
	.poll = mkrc_poll,
	.llseek = noop_llseek,
};

static void mkrc_flush_work_queue(struct mkrc_ctx *ctx)
{
	struct mkrc_msg_node *node;

	while ((node = mkrc_pop_work(ctx)) != NULL)
		kfree(node);
}

static void mkrc_flush_guest_queue(struct mkrc_ctx *ctx)
{
	struct mkrc_msg_node *node;

	while ((node = mkrc_pop_guest_request(ctx)) != NULL)
		kfree(node);
}

static void mkrc_fail_pending_calls(struct mkrc_ctx *ctx)
{
	struct mkrc_pending_call *pending;
	struct mkrc_pending_call *tmp;
	unsigned long flags;

	spin_lock_irqsave(&ctx->pending_lock, flags);
	list_for_each_entry_safe(pending, tmp, &ctx->pending_calls, node) {
		list_del_init(&pending->node);
		complete(&pending->done);
	}
	spin_unlock_irqrestore(&ctx->pending_lock, flags);
}

static void mkrc_report(const struct mkrc_ctx *ctx, const char *tag)
{
	pr_info("%s: %s role=%d local=%u kernels=%u tx_ok=%lld tx_fail=%lld rx_ok=%lld rx_bad=%lld\n",
		MKRC_NAME, tag, ctx->role, ctx->info.local_id, ctx->info.kernels,
		atomic64_read(&ctx->tx_ok), atomic64_read(&ctx->tx_fail),
		atomic64_read(&ctx->rx_ok), atomic64_read(&ctx->rx_bad));
}

static int __init mkrc_init(void)
{
	int ret;

	memset(&mkrc, 0, sizeof(mkrc));
	mkrc.role = mkrc_parse_role(role);
	if (mkrc.role == MKRC_ROLE_INVALID) {
		pr_err("%s: invalid role=%s\n", MKRC_NAME, role ?: "(null)");
		return -EINVAL;
	}

	ret = mkring_get_info(&mkrc.info);
	if (ret) {
		pr_err("%s: mkring_get_info failed: %d\n", MKRC_NAME, ret);
		return ret;
	}

	if (mkrc.info.kernels > MKRING_MAX_KERNELS) {
		pr_err("%s: kernels=%u exceeds supported max=%u\n",
		       MKRC_NAME, mkrc.info.kernels, MKRING_MAX_KERNELS);
		return -EINVAL;
	}
	if (mkrc.info.msg_size < sizeof(struct mkring_container_message)) {
		pr_err("%s: mkring.msg_size=%u is smaller than container message=%zu\n",
		       MKRC_NAME, mkrc.info.msg_size,
		       sizeof(struct mkring_container_message));
		return -EMSGSIZE;
	}

	strscpy(mkrc.device_name, device_name ?: MKRC_DEFAULT_DEVNODE,
		sizeof(mkrc.device_name));

	spin_lock_init(&mkrc.work_lock);
	INIT_LIST_HEAD(&mkrc.work_queue);
	init_waitqueue_head(&mkrc.work_wq);
	atomic_set(&mkrc.work_pending, 0);

	spin_lock_init(&mkrc.guest_lock);
	INIT_LIST_HEAD(&mkrc.guest_queue);
	init_waitqueue_head(&mkrc.guest_wq);
	atomic_set(&mkrc.guest_pending, 0);

	spin_lock_init(&mkrc.pending_lock);
	INIT_LIST_HEAD(&mkrc.pending_calls);
	init_waitqueue_head(&mkrc.ready_wq);
	atomic64_set(&mkrc.next_request_id, 1);

	ret = mkring_demux_register_channel(MKRING_CHANNEL_CONTAINER,
					    &mkrc_demux_ops, &mkrc);
	if (ret) {
		pr_err("%s: mkring_demux_register_channel(container) failed: %d\n",
		       MKRC_NAME, ret);
		goto err_out;
	}

	mkrc.worker = kthread_run(mkrc_worker_thread, &mkrc, "mkrc-worker");
	if (IS_ERR(mkrc.worker)) {
		ret = PTR_ERR(mkrc.worker);
		mkrc.worker = NULL;
		pr_err("%s: failed to start worker: %d\n", MKRC_NAME, ret);
		goto err_unreg_channel;
	}

	mkrc.miscdev.minor = MISC_DYNAMIC_MINOR;
	mkrc.miscdev.name = mkrc.device_name;
	mkrc.miscdev.fops = &mkrc_fops;
	mkrc.miscdev.mode = 0600;

	ret = misc_register(&mkrc.miscdev);
	if (ret) {
		pr_err("%s: misc_register(%s) failed: %d\n",
		       MKRC_NAME, mkrc.device_name, ret);
		goto err_worker;
	}

	mkrc_report(&mkrc, "started");
	pr_info("%s: device=/dev/%s role=%s local=%u kernels=%u msg_size=%u\n",
		MKRC_NAME, mkrc.device_name,
		mkrc.role == MKRC_ROLE_HOST ? "host" : "guest",
		mkrc.info.local_id, mkrc.info.kernels, mkrc.info.msg_size);
	return 0;

err_worker:
	mkrc.stopping = true;
	wake_up_interruptible(&mkrc.work_wq);
	if (mkrc.worker) {
		kthread_stop(mkrc.worker);
		mkrc.worker = NULL;
	}
err_unreg_channel:
	mkring_demux_unregister_channel(MKRING_CHANNEL_CONTAINER);
err_out:
	mkrc_flush_work_queue(&mkrc);
	mkrc_flush_guest_queue(&mkrc);
	return ret;
}

static void __exit mkrc_exit(void)
{
	mkrc.stopping = true;
	wake_up_interruptible(&mkrc.work_wq);
	wake_up_interruptible(&mkrc.guest_wq);
	wake_up_interruptible(&mkrc.ready_wq);

	mkrc_fail_pending_calls(&mkrc);

	if (mkrc.worker) {
		kthread_stop(mkrc.worker);
		mkrc.worker = NULL;
	}

	misc_deregister(&mkrc.miscdev);

	mkring_demux_unregister_channel(MKRING_CHANNEL_CONTAINER);

	mkrc_flush_work_queue(&mkrc);
	mkrc_flush_guest_queue(&mkrc);
	mkrc_report(&mkrc, "exit");
}

module_init(mkrc_init);
module_exit(mkrc_exit);

MODULE_LICENSE("GPL");
MODULE_AUTHOR("yezhucan");
MODULE_DESCRIPTION("Container control bridge for mkring");
