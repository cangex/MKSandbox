# Kbuild fragment (core built-in, test as loadable module)
obj-y += mkring.o
obj-y += init_mk.o
obj-y += mkring_demux.o
obj-m += mkring_test.o
obj-m += mkring_stream_bridge.o

# Phase 2 staging file kept out of the local build until the direct entry is
# moved into the Linux source tree:
# - mkring_transport_syscall.c
