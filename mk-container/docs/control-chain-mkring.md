# Mkring Control Chain

## Goal

Use `mkring` as the host <-> sub-kernel control path without sharing a
sub-kernel rootfs.

The control chain is:

`mkcri -> direct mkring control transport -> host kernel mkring -> sub-kernel guest agent -> containerd`

## Why a direct host control path is used

`mkring` only exposes in-kernel APIs today. `mkcri` is a user-space Go process,
so it cannot call `mkring_send()` / `mkring_recv()` directly. The current tree
uses the kernel bridge module `mkring_container_bridge.c`, and `mkcri` now
talks to that device directly through its in-process control transport. The
complete path becomes:

`mkcri -> /dev/mkring_container_bridge -> mkring -> sub-kernel /dev/mkring_container_bridge -> mk-guest-agent -> containerd`

The host runtime still needs to:

- accept CRI/runtime requests from `mkcri`,
- hand them to the host kernel's mkring bridge,
- forward them to the target sub-kernel,
- collect the response and return it to the runtime engine.

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

`pkg/agent` parses the peer-kernel-id (`7`) and sends runtime operations over
the host `mkring` control transport.

## Host-side control API expected by mkcri

The host-side transport should provide the same logical operations:

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
