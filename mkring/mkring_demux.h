#ifndef _MKRING_DEMUX_H
#define _MKRING_DEMUX_H

#include "mkring.h"
#include "mkring_proto.h"

typedef bool (*mkring_demux_validate_t)(const void *data, u32 len, void *priv);
typedef void (*mkring_demux_rx_t)(u16 src_kid, const void *data, u32 len,
				  void *priv);

struct mkring_demux_ops {
	mkring_demux_validate_t validate;
	mkring_demux_rx_t rx;
};

int mkring_demux_init(void);
void mkring_demux_exit(void);

int mkring_demux_register_channel(u8 channel,
				  const struct mkring_demux_ops *ops,
				  void *priv);
void mkring_demux_unregister_channel(u8 channel);

#endif /* _MKRING_DEMUX_H */
