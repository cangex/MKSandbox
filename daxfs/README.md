# DAXFS

## 项目简介

DAXFS 共享内存 通过直接内存访问 可挂载文件系统

## 目录结构

- `daxfs/daxfs/`：内核模块源码
- `daxfs/tools/`：daxfs命令行工具 `mkdaxfs`、`daxfs-inspect`
- `daxfs-share/`：示例输入目录 SOURCE
- `daxfs-setup.sh`：快速加载并挂载示例
- `daxfs-update.sh`：更新镜像内容示例

## 使用

在内核源码里放入code.c里的预留内存的函数 (daxfs_mem)

- 插入模块:  insmod daxfs/daxfs/daxfs.ko
- 生成daxfs:    daxfs/tools/mkdaxfs -d SOURCE -D /dev/mem -p PHYSICH -s SIZE
- 挂载daxfs:    mount -t daxfs -o phys=PHYSICH,size=SIZE none /mnt/daxfs

### cmdline启动参数
```
daxfs_mem=<size>[@<start>]
Example:
  daxfs_mem=256M@0x100000000
```
主kernel与子kernel的cmdline写入同一块daxfs_mem
在子kernel中
```
- 插入模块:  insmod daxfs.ko
- 挂载daxfs:    mount -t daxfs -o phys=PHYSICH,size=SIZE none /daxfs
```
即可读取同一块内存

挂载文件系统里的内容为生成daxfs时指定 -d SOURCE 目录里的内容




