/*
 * Staging direct-entry transport implementation.
 *
 * This file keeps the local repo's direct-entry transport core in one place so
 * it mirrors the eventual Linux-tree shape more closely. The planned Linux
 * source integration is still:
 * - move the syscall prototype to: include/linux/syscalls.h
 * - move the shared UAPI structs/opcodes to: include/uapi/linux/mkring_control.h
 * - place the implementation in: kernel/mkring_transport_syscall.c
 * - add the syscall number in the architecture syscall table, for example:
 *   arch/x86/entry/syscalls/syscall_64.tbl
 *
 * The kernel transport intentionally only provides message send/recv semantics.
 * Message validity beyond the basic transport header and all container business
 * meaning stay in userspace.
 *
 * The local mkring build still keeps this file out of Kbuild until the code is
 * actually moved into the Linux source tree.
 */

#include <linux/atomic.h>
#include <linux/errno.h>
#include <linux/jiffies.h>
#include <linux/kernel.h>
#include <linux/list.h>
#include <linux/mutex.h>
#include <linux/slab.h>
#include <linux/spinlock.h>
#include <linux/string.h>
#include <linux/syscalls.h>
#include <linux/types.h>
#include <linux/uaccess.h>
#include <linux/wait.h>

#include "mkring.h"
#include "mkring_transport_syscall.h"
#include "mkring_transport_uapi.h"
#include "mkring_demux.h"
#include "mkring_proto.h"

#define MKCT_NAME "mkring-control"

struct mkct_wire_prefix {
	u32 magic;
	u8 version;
	u8 channel;
} __attribute__((packed));

struct mkct_msg_node {
	struct list_head node;
	u16 peer_kernel_id;
	u32 message_len;
	u8 message[MKRING_TRANSPORT_MAX_MESSAGE];
};

struct mkct_rx_queue {
	spinlock_t lock;
	struct list_head messages;
	wait_queue_head_t wq;
	atomic_t pending;
};

struct mkct_ctx {
	struct mkring_info info;
	bool started;
	bool stopping;
	struct mkct_rx_queue queues[MKRING_CHANNEL_NET + 1];
	atomic64_t rx_ok;
	atomic64_t rx_bad;
	atomic64_t tx_ok;
	atomic64_t tx_fail;
};

static struct mkct_ctx mkct;
static DEFINE_MUTEX(mkct_init_lock);

static bool mkct_supported_channel(u16 channel)
{
	return channel == MKRING_CHANNEL_CONTAINER ||
	       channel == MKRING_CHANNEL_STREAM ||
	       channel == MKRING_CHANNEL_NET;
}

static bool mkct_valid_peer(const struct mkct_ctx *ctx, u16 peer_kernel_id)
{
	return ctx && peer_kernel_id < ctx->info.kernels &&
	       peer_kernel_id != ctx->info.local_id;
}

static bool mkct_valid_message(const void *data, u32 len)
{
	const struct mkct_wire_prefix *hdr = data;

	if (len > MKRING_TRANSPORT_MAX_MESSAGE)
		return false;
	if (!hdr || len < sizeof(*hdr))
		return false;
	if (hdr->magic != MKRING_PROTO_MAGIC)
		return false;
	if (hdr->version != MKRING_PROTO_VERSION)
		return false;
	if (!mkct_supported_channel(hdr->channel))
		return false;
	return true;
}

static void mkct_queue_init(struct mkct_rx_queue *queue)
{
	spin_lock_init(&queue->lock);
	INIT_LIST_HEAD(&queue->messages);
	init_waitqueue_head(&queue->wq);
	atomic_set(&queue->pending, 0);
}

static struct mkct_msg_node *mkct_pop_message(struct mkct_rx_queue *queue)
{
	struct mkct_msg_node *node = NULL;
	unsigned long flags;

	spin_lock_irqsave(&queue->lock, flags);
	if (!list_empty(&queue->messages)) {
		node = list_first_entry(&queue->messages, struct mkct_msg_node, node);
		list_del(&node->node);
		atomic_dec(&queue->pending);
	}
	spin_unlock_irqrestore(&queue->lock, flags);
	return node;
}

static void mkct_queue_message(struct mkct_rx_queue *queue,
			      struct mkct_msg_node *node)
{
	unsigned long flags;

	spin_lock_irqsave(&queue->lock, flags);
	list_add_tail(&node->node, &queue->messages);
	atomic_inc(&queue->pending);
	spin_unlock_irqrestore(&queue->lock, flags);
	wake_up_interruptible(&queue->wq);
}

static bool mkct_demux_validate(const void *data, u32 len, void *priv)
{
	const struct mkct_ctx *ctx = priv;

	if (!ctx || !data)
		return false;
	return mkct_valid_message(data, len);
}

static void mkct_demux_rx(u16 src_kid, const void *data, u32 len, void *priv)
{
	struct mkct_ctx *ctx = priv;
	const struct mkct_wire_prefix *hdr = data;
	struct mkct_msg_node *node;

	if (!ctx || !data || !mkct_valid_peer(ctx, src_kid) ||
	    !mkct_valid_message(data, len)) {
		if (ctx)
			atomic64_inc(&ctx->rx_bad);
		return;
	}
	if (!mkct_supported_channel(hdr->channel)) {
		atomic64_inc(&ctx->rx_bad);
		return;
	}

	node = kzalloc(sizeof(*node), GFP_ATOMIC);
	if (!node) {
		atomic64_inc(&ctx->rx_bad);
		return;
	}

	INIT_LIST_HEAD(&node->node);
	node->peer_kernel_id = src_kid;
	node->message_len = len;
	memcpy(node->message, data, len);
	atomic64_inc(&ctx->rx_ok);
	mkct_queue_message(&ctx->queues[hdr->channel], node);
}

static const struct mkring_demux_ops mkct_demux_ops = {
	.validate = mkct_demux_validate,
	.rx = mkct_demux_rx,
};

static int mkct_register_demux(struct mkct_ctx *ctx)
{
	int ret;

	ret = mkring_demux_register_channel(MKRING_CHANNEL_CONTAINER,
					    &mkct_demux_ops, ctx);
	if (ret)
		return ret;

	ret = mkring_demux_register_channel(MKRING_CHANNEL_STREAM,
					    &mkct_demux_ops, ctx);
	if (ret) {
		mkring_demux_unregister_channel(MKRING_CHANNEL_CONTAINER);
		return ret;
	}
	ret = mkring_demux_register_channel(MKRING_CHANNEL_NET,
					    &mkct_demux_ops, ctx);
	if (ret) {
		mkring_demux_unregister_channel(MKRING_CHANNEL_STREAM);
		mkring_demux_unregister_channel(MKRING_CHANNEL_CONTAINER);
		return ret;
	}

	return 0;
}

static int mkct_ensure_started(struct mkct_ctx *ctx)
{
	int ret;

	if (!ctx)
		return -EINVAL;
	if (ctx->started)
		return 0;

	mutex_lock(&mkct_init_lock);
	if (ctx->started) {
		mutex_unlock(&mkct_init_lock);
		return 0;
	}

	memset(ctx, 0, sizeof(*ctx));
	ret = mkring_get_info(&ctx->info);
	if (ret)
		goto out_unlock;
	if (ctx->info.msg_size < sizeof(struct mkring_proto_header)) {
		ret = -EMSGSIZE;
		goto out_unlock;
	}

	mkct_queue_init(&ctx->queues[MKRING_CHANNEL_CONTAINER]);
	mkct_queue_init(&ctx->queues[MKRING_CHANNEL_STREAM]);
	mkct_queue_init(&ctx->queues[MKRING_CHANNEL_NET]);
	atomic64_set(&ctx->rx_ok, 0);
	atomic64_set(&ctx->rx_bad, 0);
	atomic64_set(&ctx->tx_ok, 0);
	atomic64_set(&ctx->tx_fail, 0);

	ret = mkct_register_demux(ctx);
	if (ret)
		goto out_unlock;

	ctx->started = true;

out_unlock:
	mutex_unlock(&mkct_init_lock);
	return ret;
}

static int mkct_send(struct mkct_ctx *ctx, const struct mkring_transport_send *req)
{
	const struct mkct_wire_prefix *hdr;
	int ret;

	if (!ctx || !req)
		return -EINVAL;
	if (!mkct_valid_peer(ctx, req->peer_kernel_id))
		return -EINVAL;
	if (!mkct_supported_channel(req->channel))
		return -EINVAL;
	if (req->message_len == 0 || req->message_len > MKRING_TRANSPORT_MAX_MESSAGE)
		return -EMSGSIZE;
	if (!mkct_valid_message(req->message, req->message_len))
		return -EPROTO;

	hdr = (const struct mkct_wire_prefix *)req->message;
	if (hdr->channel != req->channel)
		return -EINVAL;

	ret = mkring_send(req->peer_kernel_id, req->message, req->message_len);
	if (ret) {
		atomic64_inc(&ctx->tx_fail);
		return ret;
	}

	atomic64_inc(&ctx->tx_ok);
	return 0;
}

static int mkct_recv(struct mkct_ctx *ctx, struct mkring_transport_recv *req)
{
	struct mkct_rx_queue *queue;
	struct mkct_msg_node *node;
	long wret;

	if (!ctx || !req)
		return -EINVAL;
	if (!mkct_supported_channel(req->channel))
		return -EINVAL;

	queue = &ctx->queues[req->channel];
	if (req->timeout_ms == 0) {
		wret = wait_event_interruptible(queue->wq,
					       atomic_read(&queue->pending) > 0 ||
					       ctx->stopping);
		if (wret < 0)
			return (int)wret;
	} else {
		wret = wait_event_interruptible_timeout(
			queue->wq,
			atomic_read(&queue->pending) > 0 || ctx->stopping,
			msecs_to_jiffies(req->timeout_ms));
		if (wret < 0)
			return (int)wret;
		if (wret == 0)
			return -ETIMEDOUT;
	}

	if (ctx->stopping)
		return -ESHUTDOWN;

	node = mkct_pop_message(queue);
	if (!node)
		return -EAGAIN;

	req->peer_kernel_id = node->peer_kernel_id;
	req->message_len = node->message_len;
	memcpy(req->message, node->message, node->message_len);
	kfree(node);
	return 0;
}

static long mkct_dispatch_user(struct mkct_ctx *ctx, __u32 op, void __user *arg)
{
	if (!ctx || !arg)
		return -EINVAL;

	switch (op) {
	case MKRING_TRANSPORT_OP_SEND: {
		struct mkring_transport_send req;

		if (copy_from_user(&req, arg, sizeof(req)))
			return -EFAULT;
		return mkct_send(ctx, &req);
	}
	case MKRING_TRANSPORT_OP_RECV: {
		struct mkring_transport_recv req;
		int ret;

		if (copy_from_user(&req, arg, sizeof(req)))
			return -EFAULT;
		ret = mkct_recv(ctx, &req);
		if (ret)
			return ret;
		if (copy_to_user(arg, &req, sizeof(req)))
			return -EFAULT;
		return 0;
	}
	default:
		return -EINVAL;
	}
}

SYSCALL_DEFINE2(mkring_transport, u32, op, void __user *, arg)
{
	int ret;

	ret = mkct_ensure_started(&mkct);
	if (ret)
		return ret;
	return mkct_dispatch_user(&mkct, op, arg);
}
