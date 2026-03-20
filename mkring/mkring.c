#include <linux/bitmap.h>
#include <linux/bitops.h>
#include <linux/delay.h>
#include <linux/errno.h>
#include <linux/init.h>
#include <linux/io.h>
#include <linux/ioport.h>
#include <linux/jiffies.h>
#include <linux/kernel.h>
#include <linux/list.h>
#include <linux/export.h>
#include <linux/mutex.h>
#include <linux/overflow.h>
#include <linux/slab.h>
#include <linux/spinlock.h>
#include <linux/string.h>
#include <linux/types.h>
#include <linux/wait.h>

#include "mkring.h"

#define MKRING_MAGIC			0x4d4b5247U
#define MKRING_VERSION			1U
#define MKRING_ALIGN			64U
#define MKRING_READY_WAIT_MS		5000U

#define MKRING_SHM_UNINIT		0U
#define MKRING_SHM_INITING		1U
#define MKRING_SHM_READY		2U

struct mkring_desc {
	__le64 addr;
	__le32 len;
	__le16 flags;
	__le16 next;
} __packed;

struct mkring_avail_hdr {
	__le16 flags;
	__le16 idx;
} __packed;

struct mkring_used_elem {
	__le32 id;
	__le32 len;
} __packed;

struct mkring_used_hdr {
	__le16 flags;
	__le16 idx;
} __packed;

struct mkring_layout {
	u32 desc_off;
	u32 avail_off;
	u32 avail_ring_off;
	u32 avail_event_off;
	u32 used_off;
	u32 used_ring_off;
	u32 used_event_off;
	u32 data_off;
	u32 queue_size;
};

struct mkring_shm_hdr {
	u32 magic;
	u16 version;
	u16 hdr_len;
	u16 kernels;
	u16 desc_num;
	u32 msg_size;
	u32 queue_size;
	u32 total_size;
	u32 init_state;
	u32 ready_bitmap;
	u32 reserved[6];
};

struct mkring_rx_msg {
	struct list_head node;
	u32 len;
	u8 data[];
};

struct mkring_ctx;

struct mkring_queue {
	struct mkring_ctx *ctx;
	u16 remote_id;
	u32 qindex;
	void *base;
	phys_addr_t phys;

	unsigned long *inflight;
	u16 free_cnt;
	u16 last_used_idx;
	u16 last_avail_idx;

	spinlock_t tx_lock;
	spinlock_t rx_lock;
	spinlock_t proc_lock;
	struct list_head rx_msgs;
	wait_queue_head_t rx_wq;
	atomic_t rx_pending;
	mkring_rx_cb_t cb;
	void *cb_priv;
};

struct mkring_ctx {
	phys_addr_t shm_phys;
	size_t shm_size;
	struct resource *shm_res;
	void *shm_base;
	struct mkring_shm_hdr *hdr;
	size_t hdr_len;

	u16 local_id;
	u16 kernels;
	u16 desc_num;
	u32 msg_size;
	struct mkring_layout layout;

	struct mkring_queue *txq;
	struct mkring_queue *rxq;

	spinlock_t ipc_lock;
	mkring_ipi_notify_t notify;
	void *notify_priv;

	bool force_init;
	bool ready;
};

static struct mkring_ctx *mkring;
static DEFINE_MUTEX(mkring_init_lock);

static inline void *mkring_ptr(const struct mkring_queue *q, u32 off)
{
	return (u8 *)q->base + off;
}

static inline struct mkring_desc *mkring_desc_ptr(const struct mkring_queue *q,
						  u16 id)
{
	return (struct mkring_desc *)(mkring_ptr(q, q->ctx->layout.desc_off)) + id;
}

static inline __le16 *mkring_avail_idx_ptr(const struct mkring_queue *q)
{
	return (__le16 *)(mkring_ptr(q, q->ctx->layout.avail_off) +
			  offsetof(struct mkring_avail_hdr, idx));
}

static inline __le16 *mkring_avail_ring_ptr(const struct mkring_queue *q,
					    u16 slot)
{
	return (__le16 *)(mkring_ptr(q, q->ctx->layout.avail_ring_off) +
			  sizeof(__le16) * slot);
}

static inline __le16 *mkring_used_idx_ptr(const struct mkring_queue *q)
{
	return (__le16 *)(mkring_ptr(q, q->ctx->layout.used_off) +
			  offsetof(struct mkring_used_hdr, idx));
}

static inline struct mkring_used_elem *mkring_used_elem_ptr(
	const struct mkring_queue *q, u16 slot)
{
	return (struct mkring_used_elem *)(mkring_ptr(q, q->ctx->layout.used_ring_off) +
					   sizeof(struct mkring_used_elem) * slot);
}

static inline void *mkring_data_ptr(const struct mkring_queue *q, u16 id)
{
	return mkring_ptr(q, q->ctx->layout.data_off + id * q->ctx->msg_size);
}

static inline u64 mkring_data_phys(const struct mkring_queue *q, u16 id)
{
	return (u64)q->phys + q->ctx->layout.data_off + id * q->ctx->msg_size;
}

static inline u32 mkring_qindex(const struct mkring_ctx *ctx, u16 src, u16 dst)
{
	return src * ctx->kernels + dst;
}

static int mkring_calc_layout(u16 ndesc, u32 payload, struct mkring_layout *out)
{
	u32 off = 0;
	u32 desc_bytes;
	u32 avail_bytes;
	u32 used_bytes;
	u32 data_bytes;

	if (!ndesc || !payload)
		return -EINVAL;

	if (check_mul_overflow((u32)ndesc, (u32)sizeof(struct mkring_desc),
			       &desc_bytes))
		return -EOVERFLOW;

	if (check_mul_overflow((u32)ndesc, (u32)sizeof(__le16), &avail_bytes))
		return -EOVERFLOW;
	avail_bytes += sizeof(struct mkring_avail_hdr) + sizeof(__le16);

	if (check_mul_overflow((u32)ndesc,
			       (u32)sizeof(struct mkring_used_elem),
			       &used_bytes))
		return -EOVERFLOW;
	used_bytes += sizeof(struct mkring_used_hdr) + sizeof(__le16);

	out->desc_off = off;
	off += desc_bytes;
	out->avail_off = ALIGN(off, 2);
	out->avail_ring_off = out->avail_off + sizeof(struct mkring_avail_hdr);
	out->avail_event_off = out->avail_ring_off + sizeof(__le16) * ndesc;

	off = out->avail_off + avail_bytes;
	out->used_off = ALIGN(off, 4);
	out->used_ring_off = out->used_off + sizeof(struct mkring_used_hdr);
	out->used_event_off =
		out->used_ring_off + sizeof(struct mkring_used_elem) * ndesc;

	off = out->used_off + used_bytes;
	out->data_off = ALIGN(off, MKRING_ALIGN);

	if (check_mul_overflow((u32)ndesc, payload, &data_bytes))
		return -EOVERFLOW;
	if (check_add_overflow(out->data_off, data_bytes, &off))
		return -EOVERFLOW;
	out->queue_size = ALIGN(off, PAGE_SIZE);

	return 0;
}

static int mkring_wait_shm_ready(struct mkring_shm_hdr *hdr)
{
	unsigned long deadline = jiffies + msecs_to_jiffies(MKRING_READY_WAIT_MS);

	while (time_before(jiffies, deadline)) {
		if (READ_ONCE(hdr->init_state) == MKRING_SHM_READY)
			return 0;
		cpu_relax();
		usleep_range(500, 1000);
	}

	return -ETIMEDOUT;
}

static bool mkring_hdr_compatible(const struct mkring_ctx *ctx)
{
	const struct mkring_shm_hdr *hdr = ctx->hdr;

	return hdr->magic == MKRING_MAGIC &&
	       hdr->version == MKRING_VERSION &&
	       hdr->kernels == ctx->kernels &&
	       hdr->desc_num == ctx->desc_num &&
	       hdr->msg_size == ctx->msg_size &&
	       hdr->queue_size == ctx->layout.queue_size;
}

static void mkring_init_one_queue(const struct mkring_ctx *ctx, void *qbase,
				  phys_addr_t qphys)
{
	struct mkring_queue qtmp = {
		.ctx = (struct mkring_ctx *)ctx,
		.base = qbase,
		.phys = qphys,
	};
	u16 i;

	memset(qbase, 0, ctx->layout.queue_size);

	for (i = 0; i < ctx->desc_num; i++) {
		struct mkring_desc *d = mkring_desc_ptr(&qtmp, i);

		WRITE_ONCE(d->addr, cpu_to_le64(mkring_data_phys(&qtmp, i)));
		WRITE_ONCE(d->len, cpu_to_le32(ctx->msg_size));
		WRITE_ONCE(d->flags, cpu_to_le16(0));
		WRITE_ONCE(d->next, cpu_to_le16(0));
	}
}

static int mkring_prepare_shared(struct mkring_ctx *ctx)
{
	struct mkring_shm_hdr *hdr = ctx->hdr;
	size_t queue_count;
	size_t queue_bytes;
	size_t required;
	u64 off;
	u32 old;
	u16 src, dst;
	int ret;

	if (check_mul_overflow((size_t)ctx->kernels, (size_t)ctx->kernels,
			       &queue_count))
		return -EOVERFLOW;
	if (check_mul_overflow(queue_count, (size_t)ctx->layout.queue_size,
			       &queue_bytes))
		return -EOVERFLOW;
	if (check_add_overflow(ctx->hdr_len, queue_bytes, &required))
		return -EOVERFLOW;
	if (ctx->shm_size < required)
		return -EINVAL;
	if (required > U32_MAX)
		return -EOVERFLOW;

	if (ctx->force_init && ctx->local_id == 0)
		memset(ctx->shm_base, 0, required);

	if (READ_ONCE(hdr->init_state) == MKRING_SHM_READY &&
	    mkring_hdr_compatible(ctx))
		goto ready;

	old = cmpxchg(&hdr->init_state, MKRING_SHM_UNINIT, MKRING_SHM_INITING);
	if (old == MKRING_SHM_UNINIT) {
		memset(ctx->shm_base, 0, required);
		hdr->magic = MKRING_MAGIC;
		hdr->version = MKRING_VERSION;
		hdr->hdr_len = ctx->hdr_len;
		hdr->kernels = ctx->kernels;
		hdr->desc_num = ctx->desc_num;
		hdr->msg_size = ctx->msg_size;
		hdr->queue_size = ctx->layout.queue_size;
		hdr->total_size = required;

		for (src = 0; src < ctx->kernels; src++) {
			for (dst = 0; dst < ctx->kernels; dst++) {
				off = ctx->hdr_len +
				      (u64)mkring_qindex(ctx, src, dst) *
					      ctx->layout.queue_size;
				mkring_init_one_queue(ctx, (u8 *)ctx->shm_base + off,
						      ctx->shm_phys + off);
			}
		}

		smp_wmb();
		WRITE_ONCE(hdr->init_state, MKRING_SHM_READY);
	} else {
		if (old == MKRING_SHM_INITING) {
			ret = mkring_wait_shm_ready(hdr);
			if (ret)
				return ret;
		}

		if (READ_ONCE(hdr->init_state) != MKRING_SHM_READY)
			return -EAGAIN;
	}

	if (!mkring_hdr_compatible(ctx))
		return -EPROTO;

ready:
	old = READ_ONCE(hdr->ready_bitmap);
	while (cmpxchg(&hdr->ready_bitmap, old, old | BIT(ctx->local_id)) != old)
		old = READ_ONCE(hdr->ready_bitmap);

	return 0;
}

static int mkring_queue_setup(struct mkring_ctx *ctx, struct mkring_queue *q,
			      u16 remote, u16 src, u16 dst, bool need_bitmap)
{
	u64 off;

	q->ctx = ctx;
	q->remote_id = remote;
	q->qindex = mkring_qindex(ctx, src, dst);

	off = ctx->hdr_len + (u64)q->qindex * ctx->layout.queue_size;
	if (off + ctx->layout.queue_size > ctx->shm_size)
		return -ERANGE;

	q->base = (u8 *)ctx->shm_base + off;
	q->phys = ctx->shm_phys + off;

	spin_lock_init(&q->tx_lock);
	spin_lock_init(&q->rx_lock);
	spin_lock_init(&q->proc_lock);
	INIT_LIST_HEAD(&q->rx_msgs);
	init_waitqueue_head(&q->rx_wq);
	atomic_set(&q->rx_pending, 0);

	q->free_cnt = ctx->desc_num;
	q->last_used_idx = le16_to_cpu(READ_ONCE(*mkring_used_idx_ptr(q)));
	q->last_avail_idx = le16_to_cpu(READ_ONCE(*mkring_avail_idx_ptr(q)));
	q->cb = NULL;
	q->cb_priv = NULL;

	if (need_bitmap) {
		q->inflight = bitmap_zalloc(ctx->desc_num, GFP_KERNEL);
		if (!q->inflight)
			return -ENOMEM;
	}

	return 0;
}

static void mkring_queue_flush_msgs(struct mkring_queue *q)
{
	struct mkring_rx_msg *msg, *tmp;
	unsigned long flags;

	spin_lock_irqsave(&q->rx_lock, flags);
	list_for_each_entry_safe(msg, tmp, &q->rx_msgs, node) {
		list_del(&msg->node);
		kfree(msg);
	}
	atomic_set(&q->rx_pending, 0);
	spin_unlock_irqrestore(&q->rx_lock, flags);
}

static void mkring_tx_reclaim_locked(struct mkring_queue *q)
{
	u16 used_idx;

	if (!q->inflight)
		return;

	used_idx = le16_to_cpu(READ_ONCE(*mkring_used_idx_ptr(q)));
	if (used_idx == q->last_used_idx)
		return;

	dma_rmb();
	while (q->last_used_idx != used_idx) {
		u16 slot = q->last_used_idx % q->ctx->desc_num;
		struct mkring_used_elem *elem = mkring_used_elem_ptr(q, slot);
		u32 id = le32_to_cpu(READ_ONCE(elem->id));

		if (id < q->ctx->desc_num && test_bit(id, q->inflight)) {
			__clear_bit(id, q->inflight);
			q->free_cnt++;
		}
		q->last_used_idx++;
	}
}

static int mkring_tx_enqueue(struct mkring_queue *q, const void *data, u32 len)
{
	unsigned long flags;
	u16 id, avail_idx, slot;
	struct mkring_desc *desc;

	spin_lock_irqsave(&q->tx_lock, flags);
	mkring_tx_reclaim_locked(q);

	if (!q->free_cnt) {
		spin_unlock_irqrestore(&q->tx_lock, flags);
		return -EAGAIN;
	}

	id = find_first_zero_bit(q->inflight, q->ctx->desc_num);
	if (id >= q->ctx->desc_num) {
		spin_unlock_irqrestore(&q->tx_lock, flags);
		return -EAGAIN;
	}

	__set_bit(id, q->inflight);
	q->free_cnt--;

	if (len)
		memcpy(mkring_data_ptr(q, id), data, len);
	desc = mkring_desc_ptr(q, id);
	WRITE_ONCE(desc->len, cpu_to_le32(len));

	dma_wmb();
	avail_idx = le16_to_cpu(READ_ONCE(*mkring_avail_idx_ptr(q)));
	slot = avail_idx % q->ctx->desc_num;
	WRITE_ONCE(*mkring_avail_ring_ptr(q, slot), cpu_to_le16(id));

	dma_wmb();
	WRITE_ONCE(*mkring_avail_idx_ptr(q), cpu_to_le16(avail_idx + 1));
	spin_unlock_irqrestore(&q->tx_lock, flags);

	return 0;
}

static void mkring_rx_enqueue_local(struct mkring_queue *q, const void *data,
				    u32 len)
{
	struct mkring_rx_msg *msg;
	mkring_rx_cb_t cb;
	void *cb_priv;
	unsigned long flags;

	msg = kmalloc(struct_size(msg, data, len), GFP_ATOMIC);
	if (!msg) {
		pr_warn_ratelimited("mkring: drop rx message from kernel %u (OOM)\n",
				    q->remote_id);
		return;
	}

	msg->len = len;
	memcpy(msg->data, data, len);

	spin_lock_irqsave(&q->rx_lock, flags);
	list_add_tail(&msg->node, &q->rx_msgs);
	atomic_inc(&q->rx_pending);
	cb = q->cb;
	cb_priv = q->cb_priv;
	spin_unlock_irqrestore(&q->rx_lock, flags);

	wake_up_interruptible(&q->rx_wq);
	if (cb)
		cb(q->remote_id, msg->data, len, cb_priv);
}

static u32 mkring_process_rx_queue(struct mkring_queue *q)
{
	unsigned long flags;
	u32 processed = 0;
	u16 avail_idx;

	spin_lock_irqsave(&q->proc_lock, flags);
	avail_idx = le16_to_cpu(READ_ONCE(*mkring_avail_idx_ptr(q)));

	while (q->last_avail_idx != avail_idx) {
		u16 slot = q->last_avail_idx % q->ctx->desc_num;
		u16 used_idx, used_slot;
		u16 id = le16_to_cpu(READ_ONCE(*mkring_avail_ring_ptr(q, slot)));
		u32 len = 0;
		struct mkring_desc *desc;
		struct mkring_used_elem *used_elem;

		if (id < q->ctx->desc_num) {
			desc = mkring_desc_ptr(q, id);
			dma_rmb();
			len = le32_to_cpu(READ_ONCE(desc->len));
			if (len > q->ctx->msg_size)
				len = q->ctx->msg_size;
			mkring_rx_enqueue_local(q, mkring_data_ptr(q, id), len);
		} else {
			pr_warn_ratelimited("mkring: invalid avail id %u from kernel %u\n",
					    id, q->remote_id);
		}

		used_idx = le16_to_cpu(READ_ONCE(*mkring_used_idx_ptr(q)));
		used_slot = used_idx % q->ctx->desc_num;
		used_elem = mkring_used_elem_ptr(q, used_slot);
		WRITE_ONCE(used_elem->id, cpu_to_le32(id));
		WRITE_ONCE(used_elem->len, cpu_to_le32(len));

		dma_wmb();
		WRITE_ONCE(*mkring_used_idx_ptr(q), cpu_to_le16(used_idx + 1));

		q->last_avail_idx++;
		processed++;
		avail_idx = le16_to_cpu(READ_ONCE(*mkring_avail_idx_ptr(q)));
	}

	spin_unlock_irqrestore(&q->proc_lock, flags);
	return processed;
}

int mkring_send(u16 dst_kid, const void *data, u32 len)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);
	struct mkring_queue *q;
	u32 ready_bitmap;
	mkring_ipi_notify_t notify;
	void *priv;
	int ret;

	if (!ctx || !ctx->ready)
		return -ENODEV;
	if (!data && len)
		return -EINVAL;
	if (dst_kid >= ctx->kernels || dst_kid == ctx->local_id)
		return -EINVAL;
	if (len > ctx->msg_size)
		return -EMSGSIZE;

	ready_bitmap = READ_ONCE(ctx->hdr->ready_bitmap);
	if (!(ready_bitmap & BIT(dst_kid)))
		return -ENOLINK;

	notify = READ_ONCE(ctx->notify);
	priv = READ_ONCE(ctx->notify_priv);
	if (!notify)
		return -ENOTCONN;

	q = &ctx->txq[dst_kid];
	ret = mkring_tx_enqueue(q, data, len);
	if (ret)
		return ret;

	ret = notify(ctx->local_id, dst_kid, priv);
	if (ret)
		pr_warn_ratelimited("mkring: IPI notify failed %u->%u: %d\n",
				    ctx->local_id, dst_kid, ret);
	return ret;
}
EXPORT_SYMBOL_GPL(mkring_send);

int mkring_recv(u16 src_kid, void *buf, u32 buf_len, u32 *out_len, long timeout)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);
	struct mkring_queue *q;
	struct mkring_rx_msg *msg;
	unsigned long flags;
	long wret;
	int ret = 0;

	if (!ctx || !ctx->ready)
		return -ENODEV;
	if (!out_len)
		return -EINVAL;
	if (src_kid >= ctx->kernels || src_kid == ctx->local_id)
		return -EINVAL;

	q = &ctx->rxq[src_kid];
	wret = wait_event_interruptible_timeout(q->rx_wq,
						atomic_read(&q->rx_pending) > 0,
						timeout);
	if (wret < 0)
		return (int)wret;
	if (wret == 0)
		return -ETIMEDOUT;

	spin_lock_irqsave(&q->rx_lock, flags);
	if (list_empty(&q->rx_msgs)) {
		spin_unlock_irqrestore(&q->rx_lock, flags);
		return -EAGAIN;
	}

	msg = list_first_entry(&q->rx_msgs, struct mkring_rx_msg, node);
	list_del(&msg->node);
	atomic_dec(&q->rx_pending);
	spin_unlock_irqrestore(&q->rx_lock, flags);

	*out_len = msg->len;
	if (msg->len > buf_len) {
		if (buf && buf_len)
			memcpy(buf, msg->data, buf_len);
		ret = -EMSGSIZE;
	} else if (msg->len && buf) {
		memcpy(buf, msg->data, msg->len);
	}

	kfree(msg);
	return ret;
}
EXPORT_SYMBOL_GPL(mkring_recv);

int mkring_register_rx_cb(u16 src_kid, mkring_rx_cb_t cb, void *priv)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);
	struct mkring_queue *q;
	unsigned long flags;

	if (!ctx || !ctx->ready)
		return -ENODEV;
	if (src_kid >= ctx->kernels || src_kid == ctx->local_id)
		return -EINVAL;

	q = &ctx->rxq[src_kid];
	spin_lock_irqsave(&q->rx_lock, flags);
	q->cb = cb;
	q->cb_priv = priv;
	spin_unlock_irqrestore(&q->rx_lock, flags);

	return 0;
}
EXPORT_SYMBOL_GPL(mkring_register_rx_cb);

void mkring_unregister_rx_cb(u16 src_kid)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);
	struct mkring_queue *q;
	unsigned long flags;

	if (!ctx || !ctx->ready)
		return;
	if (src_kid >= ctx->kernels || src_kid == ctx->local_id)
		return;

	q = &ctx->rxq[src_kid];
	spin_lock_irqsave(&q->rx_lock, flags);
	q->cb = NULL;
	q->cb_priv = NULL;
	spin_unlock_irqrestore(&q->rx_lock, flags);
}
EXPORT_SYMBOL_GPL(mkring_unregister_rx_cb);

int mkring_register_ipi_notify(mkring_ipi_notify_t notify, void *priv)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);
	unsigned long flags;

	if (!ctx || !ctx->ready)
		return -ENODEV;
	if (!notify)
		return -EINVAL;

	spin_lock_irqsave(&ctx->ipc_lock, flags);
	if (ctx->notify) {
		spin_unlock_irqrestore(&ctx->ipc_lock, flags);
		return -EBUSY;
	}
	ctx->notify = notify;
	ctx->notify_priv = priv;
	spin_unlock_irqrestore(&ctx->ipc_lock, flags);

	return 0;
}
EXPORT_SYMBOL_GPL(mkring_register_ipi_notify);

void mkring_unregister_ipi_notify(void)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);
	unsigned long flags;

	if (!ctx)
		return;

	spin_lock_irqsave(&ctx->ipc_lock, flags);
	ctx->notify = NULL;
	ctx->notify_priv = NULL;
	spin_unlock_irqrestore(&ctx->ipc_lock, flags);
}
EXPORT_SYMBOL_GPL(mkring_unregister_ipi_notify);

int mkring_get_info(struct mkring_info *info)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);

	if (!ctx || !ctx->ready)
		return -ENODEV;
	if (!info)
		return -EINVAL;

	memset(info, 0, sizeof(*info));
	info->local_id = ctx->local_id;
	info->kernels = ctx->kernels;
	info->desc_num = ctx->desc_num;
	info->msg_size = ctx->msg_size;
	info->ready_bitmap = READ_ONCE(ctx->hdr->ready_bitmap);
	return 0;
}
EXPORT_SYMBOL_GPL(mkring_get_info);

void mkring_handle_ipi_all(void)
{
	struct mkring_ctx *ctx = READ_ONCE(mkring);
	u16 kid;

	if (!ctx || !ctx->ready)
		return;

	for (kid = 0; kid < ctx->kernels; kid++) {
		if (kid == ctx->local_id)
			continue;
		mkring_process_rx_queue(&ctx->rxq[kid]);
	}
}
EXPORT_SYMBOL_GPL(mkring_handle_ipi_all);

void mkring_ipi_interrupt(void)
{
	mkring_handle_ipi_all();
}
EXPORT_SYMBOL_GPL(mkring_ipi_interrupt);

static void mkring_release_ctx(struct mkring_ctx *ctx)
{
	u16 i;
	u32 old;

	if (!ctx)
		return;

	if (ctx->rxq) {
		for (i = 0; i < ctx->kernels; i++) {
			if (i == ctx->local_id)
				continue;
			mkring_queue_flush_msgs(&ctx->rxq[i]);
		}
	}

	if (ctx->txq) {
		for (i = 0; i < ctx->kernels; i++) {
			if (i == ctx->local_id)
				continue;
			bitmap_free(ctx->txq[i].inflight);
		}
	}

	if (ctx->hdr) {
		old = READ_ONCE(ctx->hdr->ready_bitmap);
		while (cmpxchg(&ctx->hdr->ready_bitmap, old,
			       old & ~BIT(ctx->local_id)) != old)
			old = READ_ONCE(ctx->hdr->ready_bitmap);
	}

	if (ctx->shm_base)
		memunmap(ctx->shm_base);
	if (ctx->shm_res)
		release_mem_region(ctx->shm_phys, ctx->shm_size);

	kfree(ctx->txq);
	kfree(ctx->rxq);
	kfree(ctx);
}

int mkring_init(const struct mkring_boot_params *params)
{
	struct mkring_ctx *ctx;
	size_t hdr_len;
	u16 i;
	int ret;

	if (!params)
		return -EINVAL;
	if (!params->shm_phys || !params->shm_size)
		return -EINVAL;
	if (!params->kernels || params->kernels > MKRING_MAX_KERNELS)
		return -EINVAL;
	if (params->kernel_id >= params->kernels)
		return -EINVAL;
	if (!params->desc_num || !params->msg_size)
		return -EINVAL;

	mutex_lock(&mkring_init_lock);
	if (READ_ONCE(mkring)) {
		mutex_unlock(&mkring_init_lock);
		return -EALREADY;
	}

	ctx = kzalloc(sizeof(*ctx), GFP_KERNEL);
	if (!ctx) {
		mutex_unlock(&mkring_init_lock);
		return -ENOMEM;
	}

	ctx->shm_phys = params->shm_phys;
	ctx->shm_size = params->shm_size;
	ctx->local_id = params->kernel_id;
	ctx->kernels = params->kernels;
	ctx->desc_num = params->desc_num;
	ctx->msg_size = params->msg_size;
	ctx->force_init = params->force_init;
	spin_lock_init(&ctx->ipc_lock);

	ret = mkring_calc_layout(ctx->desc_num, ctx->msg_size, &ctx->layout);
	if (ret)
		goto err_out;

	hdr_len = ALIGN(sizeof(struct mkring_shm_hdr), MKRING_ALIGN);
	ctx->hdr_len = hdr_len;

	ctx->shm_res = request_mem_region(ctx->shm_phys, ctx->shm_size,
					  "mkring-shm");
	if (!ctx->shm_res) {
		ret = -EBUSY;
		goto err_out;
	}

	ctx->shm_base = memremap(ctx->shm_phys, ctx->shm_size, MEMREMAP_WB);
	if (!ctx->shm_base) {
		ret = -ENOMEM;
		goto err_out;
	}

	ctx->hdr = ctx->shm_base;
	ret = mkring_prepare_shared(ctx);
	if (ret)
		goto err_out;

	ctx->txq = kcalloc(ctx->kernels, sizeof(*ctx->txq), GFP_KERNEL);
	ctx->rxq = kcalloc(ctx->kernels, sizeof(*ctx->rxq), GFP_KERNEL);
	if (!ctx->txq || !ctx->rxq) {
		ret = -ENOMEM;
		goto err_out;
	}

	for (i = 0; i < ctx->kernels; i++) {
		if (i == ctx->local_id)
			continue;

		ret = mkring_queue_setup(ctx, &ctx->txq[i], i, ctx->local_id, i,
					 true);
		if (ret)
			goto err_out;

		ret = mkring_queue_setup(ctx, &ctx->rxq[i], i, i, ctx->local_id,
					 false);
		if (ret)
			goto err_out;
	}

	ctx->ready = true;
	WRITE_ONCE(mkring, ctx);

	if (params->notify) {
		ret = mkring_register_ipi_notify(params->notify,
						 params->notify_priv);
		if (ret) {
			WRITE_ONCE(mkring, NULL);
			ctx->ready = false;
			goto err_out;
		}
	}

	mutex_unlock(&mkring_init_lock);

	pr_info("mkring: ready local=%u kernels=%u desc=%u msg=%u shm=%pa size=%zu\n",
		ctx->local_id, ctx->kernels, ctx->desc_num, ctx->msg_size,
		&ctx->shm_phys, ctx->shm_size);
	return 0;

err_out:
	mkring_release_ctx(ctx);
	mutex_unlock(&mkring_init_lock);
	return ret;
}
EXPORT_SYMBOL_GPL(mkring_init);

void mkring_exit(void)
{
	struct mkring_ctx *ctx;

	mutex_lock(&mkring_init_lock);
	ctx = READ_ONCE(mkring);
	WRITE_ONCE(mkring, NULL);
	mutex_unlock(&mkring_init_lock);

	mkring_release_ctx(ctx);
	if (ctx)
		pr_info("mkring: unloaded\n");
}
EXPORT_SYMBOL_GPL(mkring_exit);
