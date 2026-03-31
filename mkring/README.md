# mkring (built-in)

`mkring` is the in-kernel communication substrate for the multikernel environment. It is a built-in kernel component, not a user-space program and not a standalone module loaded by itself.

Core properties:

- It uses a vring-like shared-memory queue layout (`desc/avail/used`).
- Data is written into shared memory and then announced to the target kernel through IPI.
- Any kernel `i -> j` (`i != j`) can communicate through an independent directional queue.
- Initialization registers the shared-memory region in `/proc/iomem` with `request_mem_region("mkring-shm")`.

## Files

- [mkring.c](mkring.c): core ring and communication implementation
- [mkring.h](mkring.h): exported APIs such as `mkring_init()` and `mkring_send()`
- [mkring_container.h](mkring_container.h): control-plane message headers and payloads
- [mkring_transport_uapi.h](mkring_transport_uapi.h): direct-entry transport UAPI
- [mkring_transport_syscall.c](mkring_transport_syscall.c): generic transport syscall shim plus `sys_mkring_transport` dispatch shape
- [mkring_stream.h](mkring_stream.h): stream data-plane message headers and payloads
- [init_mk.c](init_mk.c): kernel command-line parsing and built-in initialization entrypoint
- [Makefile](Makefile): kernel-tree Kbuild fragment

## Built-in Initialization Path

`mkring` no longer uses `module_param/module_init` for the core transport. Instead:

1. `init_mk.c` parses kernel command-line parameters (`__setup`)
2. `subsys_initcall(init_mk)` triggers `init_mk()`
3. `init_mk()` builds `struct mkring_boot_params`
4. `mkring_init(&params)` initializes the shared-memory transport

If another platform wants a different initialization stage, remove `subsys_initcall` and call `init_mk()` manually.

## Kernel Command-Line Parameters

`init_mk.c` supports the following `mkring.*` parameters:

- `mkring.shm_phys=` (required)
- `mkring.shm_size=` (required)
- `mkring.kernel_id=` (required)
- `mkring.kernels=` (default: `2`)
- `mkring.desc_num=` (default: `256`)
- `mkring.msg_size=` (default: `1024`)
- `mkring.ipi_vector=` (default: `0xF2`)
- `mkring.ipi_dests=` (optional, comma-separated APIC physical id mapping by `kernel_id`)
- `mkring.force_init=` (default: `0`)

Example:

```text
mkring.shm_phys=0x90000000 mkring.shm_size=0x200000 mkring.kernel_id=1 mkring.kernels=4 mkring.desc_num=256 mkring.msg_size=1024 mkring.ipi_vector=0xF2 mkring.ipi_dests=0x10,0x20,0x30,0x40
```

## Public API

- `int mkring_init(const struct mkring_boot_params *params);`
- `void mkring_exit(void);`
- `int mkring_send(u16 dst_kid, const void *data, u32 len);`
- `int mkring_recv(u16 src_kid, void *buf, u32 buf_len, u32 *out_len, long timeout);`
- `int mkring_register_rx_cb(u16 src_kid, mkring_rx_cb_t cb, void *priv);`
- `int mkring_register_ipi_notify(mkring_ipi_notify_t notify, void *priv);`
- `int mkring_get_info(struct mkring_info *info);`
- `void mkring_handle_ipi_all(void);`
- `void mkring_ipi_interrupt(void);`

## Control-Plane Transport Extension

To support the `mkcri -> sub-kernel -> containerd` control chain, the tree includes a container-channel message protocol and a direct-entry transport UAPI:

- [mkring_container.h](mkring_container.h)
  - defines the container message header and payloads
  - `magic = MKRING_CONTAINER_MAGIC`
  - `channel = MKRING_CHANNEL_CONTAINER`
  - `kind = READY / REQUEST / RESPONSE`
  - `operation = CREATE / START / STOP / REMOVE / STATUS / READ_LOG / EXEC_TTY_*`
- [mkring_transport_uapi.h](mkring_transport_uapi.h)
  - defines the generic send/recv structs and opcodes used by `sys_mkring_transport`
- [mkring_transport_syscall.c](mkring_transport_syscall.c)
  - carries the transport queueing/dispatch logic and the direct-entry syscall shape

The intended boundary is:

- the kernel knows only transport concepts: `peer`, `channel`, `message bytes`, queueing, timeout, wakeup,
- the kernel does not understand container operations such as `CREATE`, `STATUS`, or `EXEC_TTY_*`,
- userspace owns message validity beyond the basic transport header,
- userspace owns `READY` semantics and request/response matching,
- `MKRING_CHANNEL_SYSTEM` remains reserved and unused.

### Userspace Interface

`sys_mkring_transport` consumes the UAPI from [mkring_transport_uapi.h](mkring_transport_uapi.h):

- `SEND`
- `RECV`

Typical sequence:

1. the sub-kernel completes `mkring_init`
2. guest userspace starts `containerd`
3. guest userspace publishes a channel-1 `READY` message with `SEND`
4. host userspace receives that message through `RECV` and marks the peer ready in userspace
5. host userspace sends a channel-1 container `REQUEST` through `SEND`
6. guest userspace receives the request with `RECV`, forwards it to `containerd.sock`, and returns the response through `SEND`

## Stream Data-Plane Extension

The TTY exec path uses a separate stream protocol:

- [mkring_stream.h](mkring_stream.h)
  - defines the stream message header and payloads
  - `channel = MKRING_CHANNEL_STREAM`
  - stream types: `STDIN`, `OUTPUT`, `CONTROL`
  - control kinds include `EXIT`

The stream packet format is interpreted in userspace. Host and guest stream
backends send and receive channel-3 packets through `sys_mkring_transport`,
while the kernel keeps only the generic transport queueing and wakeup logic.

The running system therefore does not need a separate stream bridge device.
The kernel transport layer only needs to accept channel-3 packets, queue them,
and forward them through `mkring_send()`.

## Key Data Structures

### 1. Boot Parameters: `struct mkring_boot_params`

`struct mkring_boot_params` is built by `init_mk.c` and passed into `mkring_init()`.

| Field | Purpose |
|---|---|
| `shm_phys` | shared-memory physical base address visible to all kernels |
| `shm_size` | total shared-memory size in bytes |
| `kernel_id` | logical id of the local kernel |
| `kernels` | total number of kernels in the system |
| `desc_num` | descriptor count per directional queue |
| `msg_size` | maximum size of a single message |
| `force_init` | force reset of the shared area |
| `notify` | optional IPI notify callback |
| `notify_priv` | private context for the notify callback |

### 2. Shared-Memory Global Metadata

#### `struct mkring_shm_hdr`

The shared-memory header sits at the start of the shared region and carries the globally agreed transport configuration.

| Field | Purpose |
|---|---|
| `magic` | identifies the shared area as an `mkring` region |
| `version` | layout version |
| `hdr_len` | aligned header length |
| `kernels` | number of kernels configured for the region |
| `desc_num` | descriptors per directional queue |
| `msg_size` | maximum single-message payload size |
| `queue_size` | bytes occupied by one directional queue |
| `total_size` | total bytes required by the full layout |
| `init_state` | initialization state machine |
| `ready_bitmap` | per-kernel readiness bitmap |
| `reserved[6]` | reserved extension fields |

#### `struct mkring_layout`

`mkring_layout` caches the offsets of the vring subregions inside a single directional queue.

| Field | Purpose |
|---|---|
| `desc_off` | descriptor table offset |
| `avail_off` | avail header offset |
| `avail_ring_off` | avail ring array offset |
| `avail_event_off` | avail event offset |
| `used_off` | used header offset |
| `used_ring_off` | used ring array offset |
| `used_event_off` | used event offset |
| `data_off` | payload data area offset |
| `queue_size` | total bytes of one directional queue |

### 2.1 Shared-Memory Layout

The full shared-memory layout is:

```text
[ shared header ][ queue(0,0) ][ queue(0,1) ] ... [ queue(N-1,N-1) ]
```

- `queue(src,dst)` is the one-way path from `src` to `dst`
- total queue count is `kernels * kernels`
- each queue has the same `queue_size`
- `txq[peer]` maps to `queue(local_id, peer)`
- `rxq[peer]` maps to `queue(peer, local_id)`

This is why the design is `N x N`: both directions are independent, so `A -> B` and `B -> A` never contend for the same descriptors.

### 3. Vring Building Blocks

#### `struct mkring_desc`

| Field | Purpose |
|---|---|
| `addr` | physical address of the data slot for this descriptor |
| `len` | current message length |
| `flags` | reserved flags |
| `next` | reserved next descriptor |

#### `struct mkring_avail_hdr`

| Field | Purpose |
|---|---|
| `flags` | avail ring flags |
| `idx` | producer index |

#### `struct mkring_used_elem`

| Field | Purpose |
|---|---|
| `id` | consumed descriptor id |
| `len` | consumed message length |

#### `struct mkring_used_hdr`

| Field | Purpose |
|---|---|
| `flags` | used ring flags |
| `idx` | consumer index |

### 3.1 Avail/Used Cooperation

The two rings form a submit/complete pair:

- `avail ring`: the sender publishes descriptor ids that now contain valid data
- `used ring`: the receiver publishes descriptor ids that have been fully consumed

A typical message flow is:

1. sender reclaims finished descriptors from `used`
2. sender writes payload into the selected data slot
3. sender publishes the descriptor id into `avail`
4. receiver consumes the descriptor, copies the payload into a local receive queue, and publishes the id into `used`
5. sender reclaims the descriptor on a later send

Without the `used ring`, the sender would have no safe way to know when a descriptor could be reused.

### 4. Local Queue View: `struct mkring_queue`

`struct mkring_queue` is the local runtime view of one directional queue associated with a remote peer.

| Field | Purpose |
|---|---|
| `ctx` | backpointer to the global `mkring_ctx` |
| `remote_id` | peer kernel id |
| `qindex` | global directional queue index |
| `base` | virtual base address of the queue |
| `phys` | physical base address of the queue |
| `inflight` | bitmap of in-flight TX descriptors |
| `free_cnt` | available TX descriptors |
| `last_used_idx` | sender-side reclaim progress |
| `last_avail_idx` | receiver-side consume progress |
| `tx_lock` | protects TX enqueue and reclaim |
| `rx_lock` | protects local RX structures |
| `proc_lock` | protects `mkring_process_rx_queue()` |
| `rx_msgs` | local receive-message list |
| `rx_wq` | blocking receive wait queue |
| `rx_pending` | count of local pending receive messages |
| `cb` | optional receive callback |
| `cb_priv` | receive callback private data |

### 5. Local Receive Node: `struct mkring_rx_msg`

This structure lives only in the local kernel and holds a stable copy of a received shared-memory message.

| Field | Purpose |
|---|---|
| `node` | linked-list node for `rx_msgs` |
| `len` | payload length |
| `data[]` | variable-length payload buffer |

### 6. Global Context: `struct mkring_ctx`

Each kernel instance keeps exactly one `mkring_ctx` as the local transport state.

| Field | Purpose |
|---|---|
| `shm_phys` / `shm_size` | shared-memory physical address and size |
| `shm_base` | remapped virtual address |
| `hdr` / `hdr_len` | shared header pointer and aligned length |
| `local_id` | local kernel id |
| `kernels` / `desc_num` / `msg_size` | key runtime parameters |
| `layout` | cached single-queue layout |
| `txq` | TX queue array indexed by destination kernel id |
| `rxq` | RX queue array indexed by source kernel id |
| `ipc_lock` | protects notify callback registration |
| `notify` / `notify_priv` | IPI notify callback and context |
| `force_init` | whether forced shared-area initialization is enabled |
| `ready` | whether the local instance is ready for communication |

## Data Flow Through the Core APIs

### 1. Built-in Initialization

1. kernel command line is parsed in `init_mk.c`
2. `subsys_initcall(init_mk)` runs `init_mk()`
3. `init_mk()` calls `mkring_init(&params)`
4. `mkring_init()`:
   - computes queue layout with `mkring_calc_layout()`
   - registers the shared-memory resource with `request_mem_region()`
   - maps shared memory with `memremap()`
   - initializes or validates the shared-memory layout
   - builds local `txq[peer]` and `rxq[peer]` views

### 2. Send Path (Kernel A -> Kernel B)

1. upper layer calls `mkring_send(dst_kid, data, len)`
2. `mkring_send()` checks parameters and `ready_bitmap`
3. `mkring_tx_enqueue()` reclaims finished descriptors
4. the sender copies the payload into the shared-memory data slot
5. the sender updates the descriptor and publishes into `avail`
6. the registered notify callback sends an IPI to the destination kernel

### 3. Arrival and RX Enqueue (Kernel B)

1. the target kernel receives an IPI and calls `mkring_ipi_interrupt()` or `mkring_handle_ipi_all()`
2. `mkring_process_rx_queue()` consumes new avail entries
3. payload is copied into a local receive queue through `mkring_rx_enqueue_local()`
4. the receiver writes the descriptor id into `used` so the sender can reclaim it later

### 4. Upper-Layer Receive

Two modes are supported:

- blocking receive with `mkring_recv()`
- callback-driven receive via `mkring_register_rx_cb()`

### 5. TX Descriptor Reclaim

When the receiver updates `used->idx`, the sender reclaims those descriptors during a later send through `mkring_tx_reclaim_locked()`.

## IPI Notification Model

The default notify backend uses IPI:

- sender: `mkring_send()` enqueues data and calls `notify(src, dst, priv)`
- default backend in `init_mk.c`: sends an APIC IPI to the destination kernel
- receiver: the IPI handler calls `mkring_ipi_interrupt()` or `mkring_handle_ipi_all()` to drain RX queues

`mkring_send()` checks two things before transmission:

- target readiness in `ready_bitmap`
- presence of a registered notify callback

If the destination is not ready, `mkring_send()` returns `-ENOLINK`. If no notify backend is registered, it returns `-ENOTCONN`.

## Quick Self-Test (Two Kernels)

### Test Idea

The tree includes [mkring_test.c](mkring_test.c), which repeatedly sends test traffic to a peer and prints `tx_ok/tx_fail/rx_ok/rx_bad` counters.

### Module Parameters

See the file for the test-specific `insmod` parameters.

### Typical Validation

1. boot two kernels with aligned `mkring.*` command-line parameters
2. ensure the notify backend is active
3. load the test module in both kernels
4. watch the counters grow

### Pass Criteria

- `tx_ok` continues to increase
- `rx_ok` continues to increase
- `tx_fail` stays at zero or only shows early startup noise
- `rx_bad` stays at zero

### If It Fails

Look first at:

- `ready_bitmap` and peer readiness
- notify backend wiring
- shared-memory layout mismatches
- IPI delivery
- queue-size and message-size mismatches
- demux/channel validation for higher-level bridge modules

## Notes

This directory now supports both:

- the container control plane over channel 1, and
- the TTY exec stream data plane over channel 3.

That split matters operationally:

- control-plane traffic must continue using the `mkring_proto`-style container message wrapper,
- stream traffic uses its own stream header format and must be validated by the channel-specific stream code, not by control-plane header rules.
