#!/bin/bash
set -euo pipefail

insmod daxfs/daxfs/daxfs.ko || true
daxfs/tools/mkdaxfs -d daxfs-share -D /dev/mem -p 0x250000000 -s 256M
mkdir -p /mnt/daxfs || true
sleep 0.2
mount -t daxfs -o phys=0x250000000,size=268435456 none /mnt/daxfs
echo "mount daxfs in /mnt/daxfs"
