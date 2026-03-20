# Mkring Control Chain

## Goal

Use `mkring` as the host <-> sub-kernel control path without sharing a
sub-kernel rootfs.

The control chain is:

`mkcri -> host mkring-bridge -> host kernel mkring -> sub-kernel guest agent -> containerd`

## Why a bridge is required

`mkring` only exposes in-kernel APIs today. `mkcri` is a user-space Go process,
so it cannot call `mkring_send()` / `mkring_recv()` directly. The current tree
now has a kernel bridge module `mkring_container_bridge.c`, and the remaining
host-side bridge process should sit on top of its miscdevice. The complete path
becomes:

`mkcri -> host mkring-bridge -> /dev/mkring_container_bridge -> mkring -> sub-kernel /dev/mkring_container_bridge -> mk-guest-agent -> containerd`

The host-side bridge process is still required to:

- accept user-space requests from `mkcri`,
- hand them to the host kernel's mkring bridge,
- forward them to the target sub-kernel,
- collect the response and return it to `mkcri`.

## Addressing model

- `mkcri` still generates its own opaque `kernelID` for bookkeeping.
- `mkring` requires a numeric `dst_kid`.
- `mk-container` now allocates a per-sub-kernel peer-kernel-id from
  `MK_KERNEL_PEER_ID_BASE..MK_KERNEL_PEER_ID_MAX`.
- `kernel.Manager.StartKernel()` exports both:
  - `MK_KERNEL_ID`
  - `MK_KERNEL_PEER_ID`

The boot path should pass `MK_KERNEL_PEER_ID` into guest kernel boot params as
`mkring.kernel_id=<peer-id>`.

## Endpoint contract inside mk-container

When `MK_CONTROL_TRANSPORT=mkring`, `kernel.Manager` returns endpoints like:

`mkring://7?kernel_id=<opaque-kernel-id>`

`pkg/agent` parses the peer-kernel-id (`7`) and sends runtime operations to the
host-side bridge.

## Host-side bridge API expected by mkcri

The Go client expects a local HTTP API exposed by the host-side bridge. The
bridge itself may listen on a Unix socket such as
`/run/mk-container/mkring-bridge.sock`.

Expected operations:

- `POST /v1/kernels/{peerID}/containers`
- `POST /v1/kernels/{peerID}/containers/{containerID}/start`
- `POST /v1/kernels/{peerID}/containers/{containerID}/stop`
- `POST /v1/kernels/{peerID}/containers/{containerID}/remove`

Request payloads always include `kernel_id` so the bridge can validate routing
and correlate logs with mkcri state.

Under the HTTP layer, the intended host implementation is:

- issue `MKRING_CONTAINER_IOC_WAIT_READY` before the first request to a peer
- issue `MKRING_CONTAINER_IOC_CALL` for each container operation
- let the kernel bridge module translate `CALL -> mkring REQUEST -> RESPONSE`

## Guest-side requirements

The sub-kernel should run a small guest agent that:

- reads `REQUEST` packets from `/dev/mkring_container_bridge`,
- talks to local `containerd`,
- writes `RESPONSE` packets back to `/dev/mkring_container_bridge`,
- and after `containerd` is ready, issues `MKRING_CONTAINER_IOC_SET_READY`.

This keeps `containerd.sock` local to the sub-kernel while still letting the
host control the container lifecycle.
