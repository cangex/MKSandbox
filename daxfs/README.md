# DAXFS（精简说明）

## 项目简介

DAXFS 共享内存 通过直接内存访问

## 目录结构

- `daxfs/`：内核模块源码、工具与测试
- `daxfs/tools/`：`mkdaxfs`、`daxfs-inspect`
- `daxfs-share/`：输入目录 SOURCE
- `daxfs-setup.sh`：快速加载并挂载
- `daxfs-update.sh`：更新镜像内容示例

## 使用

- 插入模块:  insmod daxfs/daxfs/daxfs.ko
- 生成daxfs:    daxfs/tools/mkdaxfs -d SOURCE -D /dev/mem -p PHYSICH -s SIZE
- 挂载daxfs:    mount -t daxfs -o phys=PHYSICH,size=SIZE none /mnt/daxfs

### cmdline启动参数
```
daxfs_mem=<size>[@<start>]
Example:
  daxfs_mem=256M@0x100000000
```






