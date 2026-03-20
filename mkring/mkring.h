#ifndef _LINUX_MKRING_H
#define _LINUX_MKRING_H

#include <linux/types.h>

#ifndef MKRING_MAX_KERNELS
#define MKRING_MAX_KERNELS 32U
#endif

typedef int (*mkring_ipi_notify_t)(u16 src_kid, u16 dst_kid, void *priv);
typedef void (*mkring_rx_cb_t)(u16 src_kid, const void *data, u32 len,
			       void *priv);

struct mkring_info {
	u16 local_id;
	u16 kernels;
	u16 desc_num;
	u32 msg_size;
	u32 ready_bitmap;
};

struct mkring_boot_params {
	phys_addr_t shm_phys;
	size_t shm_size;
	u16 kernel_id;
	u16 kernels;
	u16 desc_num;
	u32 msg_size;
	bool force_init;
	mkring_ipi_notify_t notify;
	void *notify_priv;
};

int mkring_init(const struct mkring_boot_params *params);
void mkring_exit(void);

int mkring_send(u16 dst_kid, const void *data, u32 len);
int mkring_recv(u16 src_kid, void *buf, u32 buf_len, u32 *out_len,
		long timeout);

int mkring_register_rx_cb(u16 src_kid, mkring_rx_cb_t cb, void *priv);
void mkring_unregister_rx_cb(u16 src_kid);

int mkring_register_ipi_notify(mkring_ipi_notify_t notify, void *priv);
void mkring_unregister_ipi_notify(void);

int mkring_get_info(struct mkring_info *info);

void mkring_handle_ipi_all(void);
void mkring_ipi_interrupt(void);

#endif /* _LINUX_MKRING_H */
