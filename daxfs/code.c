#include <linux/memblock.h>
#include <linux/ioport.h>
#include <linux/multikernel.h>

//in include/linux/multikernel.h line 157
extern struct resource *multikernel_get_daxfs_resource(void);
extern bool multikernel_daxfs_available(void);
//

//in kernel/multiekrnel/mem.c  line 480 - 575
static struct resource daxfs_res = {
	.name  = "daxfs Memory",
	.start = 0,
	.end   = 0,
	.flags = IORESOURCE_BUSY | IORESOURCE_MEM,
	.desc  = IORES_DESC_RESERVED
};

struct resource *multikernel_get_daxfs_resource(void)
{
	if (!daxfs_res.start)
		return NULL;

	return &daxfs_res;
}

bool multikernel_daxfs_available(void)
{
	return daxfs_res.start != 0;
}


/*
 * daxfs_mem=<size>[@<start>]
 * Example:
 *   daxfs_mem=64M@0x100000000
 */
static int __init daxfs_mem_setup(char *str)
{
	char *cur = str;
	unsigned long long size, start;
	phys_addr_t reserved_addr;

	if (!str)
		return -EINVAL;

	size = memparse(cur, &cur);
	if (size == 0) {
		pr_err("daxfs_mem: invalid size\n");
		return -EINVAL;
	}

	if (*cur == '@') {
		cur++;
		start = memparse(cur, &cur);
		if (start == 0) {
			pr_err("daxfs_mem: invalid start address\n");
			return -EINVAL;
		}
	} else if (*cur == '\0') {
		start = 0;
	} else {
		pr_err("daxfs_mem: expected '@' or end of string after size\n");
		return -EINVAL;
	}

	if (start != 0) {
		if (memblock_reserve(start, size)) {
			pr_err("daxfs_mem: failed to reserve at specified address %llx\n", start);
			return -ENOMEM;
		}
		reserved_addr = start;
	} else {
		reserved_addr = memblock_phys_alloc(size, PAGE_SIZE);
		if (!reserved_addr) {
			pr_err("daxfs_mem: failed to allocate %llu bytes\n", size);
			return -ENOMEM;
		}
	}

	daxfs_res.start = reserved_addr;
	daxfs_res.end = reserved_addr + size - 1;

	pr_info("daxfs_mem: reserved %pa-%pa (%lluMB)\n",
		&daxfs_res.start, &daxfs_res.end,
		(unsigned long long)size >> 20);

	return 0;
}
early_param("daxfs_mem", daxfs_mem_setup);


static int __init daxfs_mem_init(void)
{
	if (!daxfs_res.start)
		return 0;

	if (insert_resource(&iomem_resource, &daxfs_res)) {
		pr_warn("daxfs_mem: failed to register in /proc/iomem\n");
	} else {
		pr_info("daxfs_mem: registered in /proc/iomem\n");
	}

	return 0;
}
core_initcall(daxfs_mem_init);
