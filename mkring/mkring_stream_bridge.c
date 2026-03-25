#include <linux/atomic.h>
#include <linux/errno.h>
#include <linux/fs.h>
#include <linux/kernel.h>
#include <linux/list.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/poll.h>
#include <linux/slab.h>
#include <linux/spinlock.h>
#include <linux/string.h>
#include <linux/uaccess.h>
#include <linux/wait.h>

#include "mkring.h"
#include "mkring_demux.h"
#include "mkring_proto.h"
#include "mkring_stream.h"

#define MKRS_NAME "mkring-stream-bridge"
#define MKRS_DEFAULT_DEVNODE "mkring_stream_bridge"

struct mkrs_msg_node {
	struct list_head node;
	struct mkring_stream_packet packet;
};

struct mkrs_ctx {
	char device_name[64];
	struct mkring_info info;
	bool started;
	bool stopping;
	struct miscdevice miscdev;
	spinlock_t rx_lock;
	struct list_head rx_queue;
	wait_queue_head_t rx_wq;
	atomic_t rx_pending;
	atomic64_t rx_ok;
	atomic64_t rx_bad;
	atomic64_t tx_ok;
	atomic64_t tx_fail;
};

static char *device_name = MKRS_DEFAULT_DEVNODE;
module_param(device_name, charp, 0444);
MODULE_PARM_DESC(device_name, "Misc device node name");

static struct mkrs_ctx mkrs;

static bool mkrs_message_valid(const struct mkring_stream_message *msg, u32 len)
{
	if (!msg)
		return false;
	if (len != sizeof(*msg))
		return false;
	if (msg->hdr.magic != MKRING_STREAM_MAGIC)
		return false;
	if (msg->hdr.version != MKRING_STREAM_VERSION)
		return false;
	if (msg->hdr.channel != MKRING_STREAM_CHANNEL)
		return false;
	if (msg->hdr.payload_len > MKRING_STREAM_MAX_PAYLOAD)
		return false;
	switch (msg->hdr.stream_type) {
	case MKRING_STREAM_TYPE_STDIN:
	case MKRING_STREAM_TYPE_OUTPUT:
	case MKRING_STREAM_TYPE_CONTROL:
		return true;
	default:
		return false;
	}
}

static void mkrs_log_bad_message(const struct mkring_stream_message *msg, u32 len,
				 const char *where)
{
	if (!msg) {
		pr_warn("%s: bad_message where=%s msg=<nil> len=%u expected_len=%zu\n",
			MKRS_NAME, where ? where : "unknown", len, sizeof(*msg));
		return;
	}

	pr_warn("%s: bad_message where=%s len=%u expected_len=%zu magic=0x%x expected_magic=0x%x version=%u expected_version=%u channel=%u expected_channel=%u stream_type=%u payload_len=%u max_payload=%u session=%s\n",
		MKRS_NAME,
		where ? where : "unknown",
		len,
		sizeof(*msg),
		msg->hdr.magic,
		MKRING_STREAM_MAGIC,
		msg->hdr.version,
		MKRING_STREAM_VERSION,
		msg->hdr.channel,
		MKRING_STREAM_CHANNEL,
		msg->hdr.stream_type,
		msg->hdr.payload_len,
		MKRING_STREAM_MAX_PAYLOAD,
		msg->hdr.session_id);
}

static void mkrs_log_write_failure(struct mkrs_ctx *ctx,
				   const struct mkring_stream_packet *packet,
				   size_t count,
				   int ret,
				   const char *reason)
{
	struct mkring_info info;
	int info_rc;

	memset(&info, 0, sizeof(info));
	info_rc = mkring_get_info(&info);
	if (packet) {
		pr_warn("%s: write rejected reason=%s ret=%d count=%zu peer=%u stream_type=%u payload_len=%u session=%s local=%u kernels=%u ready_bitmap=0x%x info_rc=%d\n",
			MKRS_NAME,
			reason ? reason : "unknown",
			ret,
			count,
			packet->peer_kernel_id,
			packet->msg.hdr.stream_type,
			packet->msg.hdr.payload_len,
			packet->msg.hdr.session_id,
			info.local_id,
			info.kernels,
			info.ready_bitmap,
			info_rc);
		return;
	}

	pr_warn("%s: write rejected reason=%s ret=%d count=%zu local=%u kernels=%u ready_bitmap=0x%x info_rc=%d\n",
		MKRS_NAME,
		reason ? reason : "unknown",
		ret,
		count,
		info.local_id,
		info.kernels,
		info.ready_bitmap,
		info_rc);
}

static bool mkrs_demux_validate(const void *data, u32 len, void *priv)
{
	const struct mkrs_ctx *ctx = priv;
	const struct mkring_stream_message *msg = data;

	if (!ctx || !msg)
		return false;
	return mkrs_message_valid(msg, len);
}

static void mkrs_demux_rx(u16 src_kid, const void *data, u32 len, void *priv)
{
	struct mkrs_ctx *ctx = priv;
	struct mkrs_msg_node *node;
	unsigned long flags;

	if (!ctx || !data) {
		if (ctx)
			atomic64_inc(&ctx->rx_bad);
		return;
	}
	if (!mkrs_message_valid(data, len)) {
		atomic64_inc(&ctx->rx_bad);
		mkrs_log_bad_message(data, len, "rx");
		return;
	}

	node = kmalloc(sizeof(*node), GFP_ATOMIC);
	if (!node) {
		atomic64_inc(&ctx->rx_bad);
		pr_warn_ratelimited("%s: drop stream frame from peer=%u due to OOM\n",
				    MKRS_NAME, src_kid);
		return;
	}

	memset(node, 0, sizeof(*node));
	INIT_LIST_HEAD(&node->node);
	node->packet.peer_kernel_id = src_kid;
	memcpy(&node->packet.msg, data, sizeof(node->packet.msg));

	spin_lock_irqsave(&ctx->rx_lock, flags);
	list_add_tail(&node->node, &ctx->rx_queue);
	atomic_inc(&ctx->rx_pending);
	spin_unlock_irqrestore(&ctx->rx_lock, flags);

	atomic64_inc(&ctx->rx_ok);
	wake_up_interruptible(&ctx->rx_wq);
}

static const struct mkring_demux_ops mkrs_demux_ops = {
	.validate = mkrs_demux_validate,
	.rx = mkrs_demux_rx,
};

static struct mkrs_msg_node *mkrs_pop_rx(struct mkrs_ctx *ctx)
{
	struct mkrs_msg_node *node = NULL;
	unsigned long flags;

	spin_lock_irqsave(&ctx->rx_lock, flags);
	if (!list_empty(&ctx->rx_queue)) {
		node = list_first_entry(&ctx->rx_queue, struct mkrs_msg_node, node);
		list_del(&node->node);
		atomic_dec(&ctx->rx_pending);
	}
	spin_unlock_irqrestore(&ctx->rx_lock, flags);
	return node;
}

static ssize_t mkrs_read(struct file *file, char __user *buf, size_t count,
			 loff_t *ppos)
{
	struct mkrs_ctx *ctx = file->private_data;
	struct mkrs_msg_node *node;
	long wret;

	(void)ppos;
	if (!ctx)
		return -ENODEV;
	if (count < sizeof(struct mkring_stream_packet))
		return -EINVAL;
	if ((file->f_flags & O_NONBLOCK) && atomic_read(&ctx->rx_pending) == 0)
		return -EAGAIN;

	wret = wait_event_interruptible(ctx->rx_wq,
					 atomic_read(&ctx->rx_pending) > 0 ||
						 ctx->stopping);
	if (wret < 0)
		return wret;
	if (ctx->stopping)
		return -ESHUTDOWN;

	node = mkrs_pop_rx(ctx);
	if (!node)
		return -EAGAIN;

	if (copy_to_user(buf, &node->packet, sizeof(node->packet))) {
		kfree(node);
		return -EFAULT;
	}
	kfree(node);
	return sizeof(struct mkring_stream_packet);
}

static ssize_t mkrs_write(struct file *file, const char __user *buf, size_t count,
			  loff_t *ppos)
{
	struct mkrs_ctx *ctx = file->private_data;
	struct mkring_stream_packet packet;
	int ret;

	(void)ppos;
	if (!ctx)
		return -ENODEV;
	if (count != sizeof(packet)) {
		mkrs_log_write_failure(ctx, NULL, count, -EINVAL, "bad_count");
		return -EINVAL;
	}
	if (copy_from_user(&packet, buf, sizeof(packet)))
		return -EFAULT;
	if (!mkrs_message_valid(&packet.msg, sizeof(packet.msg))) {
		mkrs_log_bad_message(&packet.msg, sizeof(packet.msg), "write");
		mkrs_log_write_failure(ctx, &packet, count, -EINVAL, "bad_message");
		return -EINVAL;
	}

	ret = mkring_send(packet.peer_kernel_id, &packet.msg, sizeof(packet.msg));
	if (ret) {
		atomic64_inc(&ctx->tx_fail);
		mkrs_log_write_failure(ctx, &packet, count, ret, "mkring_send");
		return ret;
	}
	atomic64_inc(&ctx->tx_ok);
	return sizeof(packet);
}

static __poll_t mkrs_poll(struct file *file, poll_table *wait)
{
	struct mkrs_ctx *ctx = file->private_data;
	__poll_t mask = 0;

	if (!ctx)
		return EPOLLERR;

	poll_wait(file, &ctx->rx_wq, wait);
	if (atomic_read(&ctx->rx_pending) > 0)
		mask |= EPOLLIN | EPOLLRDNORM;
	return mask;
}

static int mkrs_open(struct inode *inode, struct file *file)
{
	(void)inode;
	file->private_data = &mkrs;
	return 0;
}

static const struct file_operations mkrs_fops = {
	.owner = THIS_MODULE,
	.open = mkrs_open,
	.read = mkrs_read,
	.write = mkrs_write,
	.poll = mkrs_poll,
	.llseek = noop_llseek,
};

static void mkrs_flush_rx_queue(struct mkrs_ctx *ctx)
{
	struct mkrs_msg_node *node;

	while ((node = mkrs_pop_rx(ctx)) != NULL)
		kfree(node);
}

int mkrs_init(void)
{
	int ret;

	memset(&mkrs, 0, sizeof(mkrs));
	spin_lock_init(&mkrs.rx_lock);
	INIT_LIST_HEAD(&mkrs.rx_queue);
	init_waitqueue_head(&mkrs.rx_wq);
	atomic_set(&mkrs.rx_pending, 0);

	ret = mkring_get_info(&mkrs.info);
	if (ret)
		return ret;

	strscpy(mkrs.device_name, device_name, sizeof(mkrs.device_name));
	mkrs.miscdev.minor = MISC_DYNAMIC_MINOR;
	mkrs.miscdev.name = mkrs.device_name;
	mkrs.miscdev.fops = &mkrs_fops;

	ret = misc_register(&mkrs.miscdev);
	if (ret)
		return ret;

	ret = mkring_demux_register_channel(MKRING_STREAM_CHANNEL,
					    &mkrs_demux_ops, &mkrs);
	if (ret) {
		misc_deregister(&mkrs.miscdev);
		return ret;
	}

	mkrs.started = true;
	pr_info("%s: started local=%u kernels=%u device=/dev/%s\n",
		MKRS_NAME, mkrs.info.local_id, mkrs.info.kernels, mkrs.device_name);
	return 0;
}

void mkrs_exit(void)
{
	if (!mkrs.started)
		return;

	mkrs.stopping = true;
	wake_up_interruptible(&mkrs.rx_wq);
	mkring_demux_unregister_channel(MKRING_STREAM_CHANNEL);
	misc_deregister(&mkrs.miscdev);
	mkrs_flush_rx_queue(&mkrs);
	mkrs.started = false;
}

static int __init mkrs_module_init(void)
{
	return mkrs_init();
}

static void __exit mkrs_module_exit(void)
{
	mkrs_exit();
}

module_init(mkrs_module_init);
module_exit(mkrs_module_exit);

MODULE_LICENSE("GPL");
MODULE_AUTHOR("yezhucan");
MODULE_DESCRIPTION("Raw stream bridge for mkring exec data-plane");
