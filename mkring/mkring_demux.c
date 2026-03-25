#include <linux/errno.h>
#include <linux/kernel.h>
#include <linux/module.h>
#include <linux/mutex.h>
#include <linux/spinlock.h>
#include <linux/string.h>

#include "mkring.h"
#include "mkring_demux.h"
#include "mkring_proto.h"

#define MKRING_DEMUX_NAME "mkring-demux"

struct mkring_demux_header_prefix {
	__u32 magic;
	__u8 version;
	__u8 channel;
} __attribute__((packed));

struct mkring_demux_handler {
	bool registered;
	struct mkring_demux_ops ops;
	void *priv;
};

struct mkring_demux_ctx {
	struct mkring_info info;
	bool started;
	bool rx_registered[MKRING_MAX_KERNELS];
	spinlock_t handler_lock;
	struct mkring_demux_handler handlers[256];
};

static DEFINE_MUTEX(mkring_demux_init_lock);
static struct mkring_demux_ctx mkring_demux;

static bool mkring_demux_valid_peer(const struct mkring_demux_ctx *ctx, u16 peer)
{
	return peer < ctx->info.kernels && peer != ctx->info.local_id;
}

static void mkring_demux_rx_cb(u16 src_kid, const void *data, u32 len, void *priv)
{
	const struct mkring_demux_header_prefix *hdr = data;
	struct mkring_demux_handler handler;
	unsigned long flags;

	if (!hdr || len < sizeof(*hdr))
		return;
	if (hdr->magic != MKRING_PROTO_MAGIC)
		return;
	if (hdr->version != MKRING_PROTO_VERSION)
		return;
	if (hdr->channel == 0)
		return;
	if (!mkring_demux_valid_peer(&mkring_demux, src_kid))
		return;

	spin_lock_irqsave(&mkring_demux.handler_lock, flags);
	handler = mkring_demux.handlers[hdr->channel];
	spin_unlock_irqrestore(&mkring_demux.handler_lock, flags);

	if (!handler.registered || !handler.ops.rx) {
		pr_warn_ratelimited("%s: no handler for channel=%u from peer=%u len=%u\n",
				    MKRING_DEMUX_NAME, hdr->channel, src_kid, len);
		return;
	}

	if (handler.ops.validate &&
	    !handler.ops.validate(data, len, handler.priv)) {
		pr_warn_ratelimited("%s: validation failed channel=%u from peer=%u len=%u\n",
				    MKRING_DEMUX_NAME, hdr->channel, src_kid, len);
		return;
	}

	handler.ops.rx(src_kid, data, len, handler.priv);
}

int mkring_demux_init(void)
{
	u16 peer;
	int ret;

	mutex_lock(&mkring_demux_init_lock);
	if (mkring_demux.started) {
		mutex_unlock(&mkring_demux_init_lock);
		return 0;
	}

	memset(&mkring_demux, 0, sizeof(mkring_demux));
	spin_lock_init(&mkring_demux.handler_lock);

	ret = mkring_get_info(&mkring_demux.info);
	if (ret) {
		mutex_unlock(&mkring_demux_init_lock);
		return ret;
	}

	if (mkring_demux.info.kernels > MKRING_MAX_KERNELS) {
		mutex_unlock(&mkring_demux_init_lock);
		return -EINVAL;
	}

	for (peer = 0; peer < mkring_demux.info.kernels; peer++) {
		if (peer == mkring_demux.info.local_id)
			continue;

		ret = mkring_register_rx_cb(peer, mkring_demux_rx_cb, NULL);
		if (ret) {
			pr_err("%s: mkring_register_rx_cb(peer=%u) failed: %d\n",
			       MKRING_DEMUX_NAME, peer, ret);
			goto err_unregister;
		}
		mkring_demux.rx_registered[peer] = true;
	}

	mkring_demux.started = true;
	pr_info("%s: started local=%u kernels=%u\n",
		MKRING_DEMUX_NAME,
		mkring_demux.info.local_id,
		mkring_demux.info.kernels);

	mutex_unlock(&mkring_demux_init_lock);
	return 0;

err_unregister:
	for (peer = 0; peer < mkring_demux.info.kernels; peer++) {
		if (!mkring_demux.rx_registered[peer])
			continue;
		mkring_unregister_rx_cb(peer);
		mkring_demux.rx_registered[peer] = false;
	}
	mutex_unlock(&mkring_demux_init_lock);
	return ret;
}
EXPORT_SYMBOL_GPL(mkring_demux_init);

void mkring_demux_exit(void)
{
	u16 peer;
	unsigned long flags;

	mutex_lock(&mkring_demux_init_lock);
	if (!mkring_demux.started) {
		mutex_unlock(&mkring_demux_init_lock);
		return;
	}

	for (peer = 0; peer < mkring_demux.info.kernels; peer++) {
		if (!mkring_demux.rx_registered[peer])
			continue;
		mkring_unregister_rx_cb(peer);
		mkring_demux.rx_registered[peer] = false;
	}

	spin_lock_irqsave(&mkring_demux.handler_lock, flags);
	memset(mkring_demux.handlers, 0, sizeof(mkring_demux.handlers));
	spin_unlock_irqrestore(&mkring_demux.handler_lock, flags);

	mkring_demux.started = false;
	mutex_unlock(&mkring_demux_init_lock);
}
EXPORT_SYMBOL_GPL(mkring_demux_exit);

int mkring_demux_register_channel(u8 channel,
				  const struct mkring_demux_ops *ops,
				  void *priv)
{
	unsigned long flags;
	int ret;

	if (!channel || !ops || !ops->rx)
		return -EINVAL;

	ret = mkring_demux_init();
	if (ret)
		return ret;

	spin_lock_irqsave(&mkring_demux.handler_lock, flags);
	if (mkring_demux.handlers[channel].registered) {
		spin_unlock_irqrestore(&mkring_demux.handler_lock, flags);
		return -EBUSY;
	}

	mkring_demux.handlers[channel].registered = true;
	mkring_demux.handlers[channel].ops = *ops;
	mkring_demux.handlers[channel].priv = priv;
	spin_unlock_irqrestore(&mkring_demux.handler_lock, flags);

	pr_info("%s: registered channel=%u\n", MKRING_DEMUX_NAME, channel);
	return 0;
}
EXPORT_SYMBOL_GPL(mkring_demux_register_channel);

void mkring_demux_unregister_channel(u8 channel)
{
	unsigned long flags;

	if (!channel)
		return;

	spin_lock_irqsave(&mkring_demux.handler_lock, flags);
	memset(&mkring_demux.handlers[channel], 0,
	       sizeof(mkring_demux.handlers[channel]));
	spin_unlock_irqrestore(&mkring_demux.handler_lock, flags);

	pr_info("%s: unregistered channel=%u\n", MKRING_DEMUX_NAME, channel);
}
EXPORT_SYMBOL_GPL(mkring_demux_unregister_channel);

MODULE_LICENSE("GPL");
MODULE_AUTHOR("yezhucan");
MODULE_DESCRIPTION("Protocol demultiplexer for mkring");
