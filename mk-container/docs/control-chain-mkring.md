# Mkring Control Chain

## Goal

Use `mkring` as the host <-> sub-kernel control path without sharing a
sub-kernel rootfs.

The control chain is:

`mkcri -> direct mkring control transport -> host kernel mkring -> sub-kernel guest agent -> containerd`

## Why a direct host control path is used

`mkring` only exposes in-kernel APIs today. `mkcri` is a user-space Go process,
so it cannot call `mkring_send()` / `mkring_recv()` directly. The current tree
uses a dedicated direct-entry syscall (`sys_mkring_transport`) to enter the
kernel transport shim. The complete path becomes:

`mkcri -> sys_mkring_transport -> mkring -> sys_mkring_transport -> mk-guest-agent -> containerd`

The host runtime still needs to:

- accept CRI/runtime requests from `mkcri`,
- hand them to the host kernel's direct `mkring` transport entry,
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

## Host-side transport API expected by mkcri

The kernel-facing syscall only provides generic transport semantics:

- `SEND(peer_kernel_id, channel, message)`
- `RECV(channel, timeout_ms)`

The host-side userspace transport built inside `mkcri` then layers container
semantics on top:

- it publishes and observes `READY` on channel 1,
- it matches `REQUEST` / `RESPONSE` messages by transport request id,
- it performs timeout handling and snapshot-side ready forcing in userspace.

## Guest-side requirements

The sub-kernel should run a small guest agent that:

- publishes a channel-1 `READY` message after local runtime init,
- receives container `REQUEST` packets through `sys_mkring_transport`,
- talks to local `containerd`,
- sends container `RESPONSE` packets back through `sys_mkring_transport`.

This keeps `containerd.sock` local to the sub-kernel while still letting the
host control the container lifecycle.
