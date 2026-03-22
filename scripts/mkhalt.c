#define _GNU_SOURCE

#include <errno.h>
#include <limits.h>
#include <linux/reboot.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/syscall.h>
#include <unistd.h>


#define LINUX_REBOOT_CMD_MULTIKERNEL_HALT 0x4D4B4C48
#define LINUX_REBOOT_MAGIC1 0xfee1dead
#define LINUX_REBOOT_MAGIC2 672274793


struct multikernel_boot_args {
	int mk_id;
};

static int halt_multikernel(int mk_id)
{
	struct multikernel_boot_args args;

	args.mk_id = mk_id;

	return syscall(SYS_reboot,
		       LINUX_REBOOT_MAGIC1,
		       LINUX_REBOOT_MAGIC2,
		       LINUX_REBOOT_CMD_MULTIKERNEL_HALT,
		       &args);
}

static int parse_mk_id(const char *s, int *out)
{
	char *end = NULL;
	long value;

	if (!s || !out)
		return -EINVAL;

	errno = 0;
	value = strtol(s, &end, 10);
	if (errno != 0)
		return -errno;
	if (end == s || *end != '\0')
		return -EINVAL;
	if (value < 0 || value > INT_MAX)
		return -ERANGE;

	*out = (int)value;
	return 0;
}

int main(int argc, char *argv[])
{
	int mk_id;
	int ret;

	if (geteuid() != 0) {
		fprintf(stderr, "run this program as root\n");
		return 1;
	}

	if (argc != 2) {
		fprintf(stderr, "usage: %s <mk_id>\n", argv[0]);
		return 1;
	}

	ret = parse_mk_id(argv[1], &mk_id);
	if (ret < 0) {
		fprintf(stderr, "invalid mk_id: %s\n", argv[1]);
		return 1;
	}

	printf("prepare to halt multikernel instance: %d\n", mk_id);

	ret = halt_multikernel(mk_id);
	if (ret < 0) {
		perror("reboot(MULTIKERNEL_HALT)");
		return 1;
	}

	printf("halt request sent for multikernel instance %d\n", mk_id);
	return 0;
}
