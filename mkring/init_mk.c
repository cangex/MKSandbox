#include <linux/errno.h>
#include <linux/init.h>
#include <linux/kernel.h>
#include <linux/kstrtox.h>
#include <linux/smp.h>
#include <linux/string.h>

#include "mkring.h"

#if defined(CONFIG_X86_LOCAL_APIC)
#include <asm/apic.h>
#endif

static struct mkring_boot_params mk_params __initdata = {
	.kernels = 2,
	.desc_num = 256,
	.msg_size = 1024,
};

static bool mk_has_shm_phys __initdata;
static bool mk_has_shm_size __initdata;
static bool mk_has_kernel_id __initdata;
static bool mk_has_ipi_dests __initdata;
static u16 mk_ipi_dests_nr __initdata;
static u32 mk_ipi_vector __initdata = 0xF2;
static u32 mk_ipi_dests[MKRING_MAX_KERNELS] __initdata;

struct mk_ipi_notify_ctx {
	u8 vector;
	u16 kernels;
	u32 apic_id[MKRING_MAX_KERNELS];
	bool valid[MKRING_MAX_KERNELS];
};

static struct mk_ipi_notify_ctx mk_ipi_notify_ctx;

mkring_ipi_notify_t __init init_mk_get_notify(void **priv);

static int mk_ipi_notify(u16 src_kid, u16 dst_kid, void *priv)
{
	struct mk_ipi_notify_ctx *ctx = priv;

	if (!ctx)
		return -EINVAL;
	if (src_kid >= ctx->kernels || dst_kid >= ctx->kernels)
		return -EINVAL;
	if (!ctx->valid[dst_kid])
		return -ENODEV;

	/*
	 * Publish shared-memory ring writes before the destination kernel takes
	 * the interrupt and starts reading descriptors.
	 */
	smp_wmb();

#if defined(CONFIG_X86_LOCAL_APIC)
	apic_icr_write(APIC_DM_FIXED | APIC_DEST_PHYSICAL | ctx->vector,
		       ctx->apic_id[dst_kid]);
	return 0;
#else
	return -EOPNOTSUPP;
#endif
}

/*
 * 平台可在其他编译单元中提供同名强符号实现，返回专用 IPI notify 回调。
 * 默认实现使用 APIC 物理目标模式发送 IPI（需要配置 mkring.ipi_dests）。
 */
mkring_ipi_notify_t __weak __init init_mk_get_notify(void **priv)
{
	u16 kid;

	if (priv)
		*priv = NULL;

#if !defined(CONFIG_X86_LOCAL_APIC)
	pr_warn("init_mk: default IPI backend requires CONFIG_X86_LOCAL_APIC\n");
	return NULL;
#endif

	if (!mk_has_ipi_dests)
		return NULL;
	if (mk_params.kernels > MKRING_MAX_KERNELS)
		return NULL;

	memset(&mk_ipi_notify_ctx, 0, sizeof(mk_ipi_notify_ctx));
	mk_ipi_notify_ctx.vector = (u8)mk_ipi_vector;
	mk_ipi_notify_ctx.kernels = mk_params.kernels;

	for (kid = 0; kid < mk_params.kernels && kid < mk_ipi_dests_nr; kid++) {
		mk_ipi_notify_ctx.apic_id[kid] = mk_ipi_dests[kid];
		mk_ipi_notify_ctx.valid[kid] = true;
	}

	if (priv)
		*priv = &mk_ipi_notify_ctx;
	return mk_ipi_notify;
}

static int __init mk_parse_shm_phys(char *str)
{
	u64 val;

	if (kstrtou64(str, 0, &val)) {
		pr_err("init_mk: invalid mkring.shm_phys=%s\n", str);
		return 1;
	}

	mk_params.shm_phys = (phys_addr_t)val;
	mk_has_shm_phys = true;
	return 1;
}
__setup("mkring.shm_phys=", mk_parse_shm_phys);

static int __init mk_parse_shm_size(char *str)
{
	u64 val;

	if (kstrtou64(str, 0, &val) || !val) {
		pr_err("init_mk: invalid mkring.shm_size=%s\n", str);
		return 1;
	}

	mk_params.shm_size = (size_t)val;
	mk_has_shm_size = true;
	return 1;
}
__setup("mkring.shm_size=", mk_parse_shm_size);

static int __init mk_parse_kernel_id(char *str)
{
	u16 val;

	if (kstrtou16(str, 0, &val)) {
		pr_err("init_mk: invalid mkring.kernel_id=%s\n", str);
		return 1;
	}

	mk_params.kernel_id = val;
	mk_has_kernel_id = true;
	return 1;
}
__setup("mkring.kernel_id=", mk_parse_kernel_id);

static int __init mk_parse_kernels(char *str)
{
	u16 val;

	if (kstrtou16(str, 0, &val) || !val) {
		pr_err("init_mk: invalid mkring.kernels=%s\n", str);
		return 1;
	}

	mk_params.kernels = val;
	return 1;
}
__setup("mkring.kernels=", mk_parse_kernels);

static int __init mk_parse_desc_num(char *str)
{
	u16 val;

	if (kstrtou16(str, 0, &val) || !val) {
		pr_err("init_mk: invalid mkring.desc_num=%s\n", str);
		return 1;
	}

	mk_params.desc_num = val;
	return 1;
}
__setup("mkring.desc_num=", mk_parse_desc_num);

static int __init mk_parse_msg_size(char *str)
{
	u32 val;

	if (kstrtou32(str, 0, &val) || !val) {
		pr_err("init_mk: invalid mkring.msg_size=%s\n", str);
		return 1;
	}

	mk_params.msg_size = val;
	return 1;
}
__setup("mkring.msg_size=", mk_parse_msg_size);

static int __init mk_parse_ipi_vector(char *str)
{
	u32 val;

	if (kstrtou32(str, 0, &val) || val < 0x20 || val > 0xFE) {
		pr_err("init_mk: invalid mkring.ipi_vector=%s\n", str);
		return 1;
	}

	mk_ipi_vector = val;
	return 1;
}
__setup("mkring.ipi_vector=", mk_parse_ipi_vector);

static int __init mk_parse_ipi_dests(char *str)
{
	char *cursor = str;
	char *tok;
	u16 idx = 0;

	while ((tok = strsep(&cursor, ",")) != NULL) {
		u32 apic_id;

		if (!*tok)
			continue;
		if (idx >= MKRING_MAX_KERNELS) {
			pr_err("init_mk: mkring.ipi_dests too long (max=%u)\n",
			       MKRING_MAX_KERNELS);
			return 1;
		}
		if (kstrtou32(tok, 0, &apic_id)) {
			pr_err("init_mk: invalid apic id in mkring.ipi_dests: %s\n",
			       tok);
			return 1;
		}
		mk_ipi_dests[idx++] = apic_id;
	}

	if (!idx) {
		pr_err("init_mk: empty mkring.ipi_dests\n");
		return 1;
	}

	mk_ipi_dests_nr = idx;
	mk_has_ipi_dests = true;
	return 1;
}
__setup("mkring.ipi_dests=", mk_parse_ipi_dests);

static int __init mk_parse_force_init(char *str)
{
	bool val;

	if (kstrtobool(str, &val)) {
		pr_err("init_mk: invalid mkring.force_init=%s\n", str);
		return 1;
	}

	mk_params.force_init = val;
	return 1;
}
__setup("mkring.force_init=", mk_parse_force_init);

/*
 * 如果你后续希望在其他 init 阶段调用，可以移除 subsys_initcall，
 * 改为在平台初始化路径里显式调用 init_mk()。
 */
static int __init init_mk(void)
{
	int ret;

	if (!mk_has_shm_phys || !mk_has_shm_size || !mk_has_kernel_id) {
		pr_err("init_mk: missing required kernel params: mkring.shm_phys, mkring.shm_size, mkring.kernel_id\n");
		return -EINVAL;
	}
	if (mk_params.kernels > MKRING_MAX_KERNELS) {
		pr_err("init_mk: mkring.kernels exceeds max=%u\n",
		       MKRING_MAX_KERNELS);
		return -EINVAL;
	}
	if (mk_has_ipi_dests && mk_ipi_dests_nr < mk_params.kernels)
		pr_warn("init_mk: mkring.ipi_dests has %u entries, kernels=%u\n",
			mk_ipi_dests_nr, mk_params.kernels);

	mk_params.notify = init_mk_get_notify(&mk_params.notify_priv);
	if (!mk_params.notify)
		pr_warn("init_mk: no IPI notify backend, mkring_send() will return -ENOTCONN\n");

	ret = mkring_init(&mk_params);
	if (ret)
		pr_err("init_mk: mkring_init failed: %d\n", ret);

	return ret;
}
subsys_initcall(init_mk);
