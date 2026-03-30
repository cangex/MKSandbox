# SPDX-License-Identifier: GPL-2.0

ifneq ($(KERNELRELEASE),)

obj-m := daxfs.o
daxfs-y := super.o inode.o dir.o file.o dax_mem.o validate.o pcache.o overlay.o
ccflags-y += -I$(src)/../include

else

KDIR ?= /home/wzx/linux 
all:
	$(MAKE) -C $(KDIR) M=$(CURDIR) modules

clean:
	$(MAKE) -C $(KDIR) M=$(CURDIR) clean
install:
	$(MAKE) -C $(KDIR) M=$(PWD) modules_install
	depmod

endif
