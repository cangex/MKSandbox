#ifndef _MKRING_TRANSPORT_SYSCALL_H
#define _MKRING_TRANSPORT_SYSCALL_H

/*
 * Draft direct-entry prototype for sys_mkring_transport.
 *
 * Planned Linux source integration:
 * - move the syscall prototype into: include/linux/syscalls.h
 * - place the syscall implementation in: kernel/sys.c
 *   (or split it into a dedicated kernel/mkring_transport_syscall.c and keep the
 *   prototype in include/linux/syscalls.h)
 * - wire the syscall number in the architecture syscall table, for example:
 *   arch/x86/entry/syscalls/syscall_64.tbl
 *
 * This local header is only a staging file so we can keep the intended direct
 * entry visible while iterating inside the mkring tree.
 */

#include <linux/compiler_types.h>
#include <linux/linkage.h>
#include <linux/types.h>

asmlinkage long sys_mkring_transport(__u32 op, void __user *arg);

#endif /* _MKRING_TRANSPORT_SYSCALL_H */
