# mk-container (multikernel + Kubernetes CRI adapter)

`mk-container` provides a Kubernetes-compatible CRI endpoint for a multikernel system where each sub-kernel runs exactly one container.

The current architecture supports an `mkring`-based host-to-guest chain:

```text
mkcri -> userspace control transport -> sys_mkring_transport -> mkring -> sys_mkring_transport -> guest agent -> containerd
```

The running kernel only provides generic transport semantics:

- `SEND` one opaque message to a peer/channel
- `RECV` one opaque message from a channel queue

Container-level meaning stays in userspace:

- `mkcri` encodes/decodes channel-1 container messages,
- `mkcri` maintains host-side peer `READY` state and request/response matching,
- `mk-guest-agent` publishes `READY`, receives `REQUEST`, and returns `RESPONSE`,
- `MKRING_CHANNEL_SYSTEM` stays reserved and unused,
- the TTY exec data plane still uses `/dev/mkring_stream_bridge`.

## What Is Implemented

- CRI gRPC server (`RuntimeService` + `ImageService`) for kubelet integration.
- One pod sandbox -> one sub-kernel mapping.
- One kernel -> one container enforcement.
- Sub-kernel lifecycle abstraction (`kernel.Manager`) using external start/stop commands.
- Peer-kernel-id allocation for `mkring` control-plane routing.
- Guest runtime abstraction (`agent.Factory`) with:
  - a runnable mock backend,
  - an `mkring` backend built on `sys_mkring_transport` (default and only runtime path).
- Pod IP allocation.
- `command/args` passthrough from CRI to the guest `ctr` invocation.
- Host-side monitor loop for:
  - long-running container status refresh,
  - guest `.cri.log` -> host `LogPath` sync,
  - pod-level auto cleanup after `EXITED && log EOF`.
- Manual `StopContainer` support without racing monitor-driven cleanup.
- TTY `Exec` support through:
  - CRI `Exec` URL generation,
  - host streaming server,
  - `mkring` stream data plane,
  - guest PTY-backed `ctr tasks exec --tty`,
  - CRI/SPDY remotecommand compatibility for `crictl exec -it`.

## Current Boundaries

- Sub-kernel shutdown works for pod cleanup, but multikernel resource reuse is not complete yet.
- CPU reclaim, instance teardown, kimage cleanup, and related host-side resource reuse are still under active development.
- Interactive containers now support TTY exec through the CRI/SPDY front-end:
  - `crictl exec -it` works,
  - `mkexecurl` remains useful for low-level debugging of `Exec` URL generation,
  - `ExecSync` is not implemented,
  - `Attach` is not implemented.

## Repository Layout

- `cmd/mkcri`: mkcri daemon entrypoint
- `cmd/mkexecurl`: helper for requesting an `Exec` URL
- `pkg/cri`: CRI service handlers
- `pkg/runtime`: pod/container lifecycle engine
- `pkg/kernel`: sub-kernel lifecycle control
- `pkg/agent`: per-kernel runtime proxy and `mkring` transport client
- `pkg/streaming`: host-side CRI/SPDY TTY exec front-end and `mkring` data-plane integration
- `docs/`: architecture and networking details

## Quick Start

```bash
go get google.golang.org/grpc@v1.40.0
go get k8s.io/cri-api@v0.23.0
go mod tidy
go test ./...
MKCRI_TRANSPORT_SYSCALL_NR=<sys_mkring_transport_number> go run ./cmd/mkcri
```

By default, mkcri listens on `unix:///tmp/mkcri.sock`.
When `MK_CONTROL_TRANSPORT=mkring`, `MKCRI_TRANSPORT_SYSCALL_NR` must match the
running kernel's `sys_mkring_transport` syscall number.

## Environment Variables

- `MKCRI_LISTEN_SOCKET` (default: `/tmp/mkcri.sock`)
- `MKCRI_STREAM_DEVICE_PATH` (default: `/dev/mkring_stream_bridge`)
- `MK_KERNEL_START_COMMAND` (optional: command for booting a sub-kernel, receives `MK_KERNEL_ID` and `MK_KERNEL_PEER_ID`)
  - mkcri also exports `MK_KERNEL_BOOT_MODE` to this command
  - expected values:
    - `cold_boot`
    - `snapshot`
- `MK_KERNEL_STOP_COMMAND` (optional: command for stopping a sub-kernel)
- `MK_CONTROL_TRANSPORT` (default: `mkring`, supported: `mock`, `mkring`)
- `MKCRI_TRANSPORT_SYSCALL_NR` (required when `MK_CONTROL_TRANSPORT=mkring`)
- `MK_KERNEL_PEER_ID_BASE` (default: `1`)
- `MK_KERNEL_PEER_ID_MAX` (default: `255`)
- `MK_POD_CIDR_BASE` (default: `10.240.0.0`)
- `MK_POD_CIDR_MASK` (default: `24`)
- `MK_RUNTIME_NAME` (default: `mkcri`)
- `MK_RUNTIME_VERSION` (default: `0.1.0`)

When `MK_CONTROL_TRANSPORT=mkring`, `mkcri` allocates a numeric peer-kernel-id for each sub-kernel and exports it to start/stop commands as `MK_KERNEL_PEER_ID`. Boot scripts should wire that value to the guest kernel's `mkring.kernel_id`. The control-plane runtime path also requires `MKCRI_TRANSPORT_SYSCALL_NR` to match the running kernel's `sys_mkring_transport` syscall number.

Per-pod boot mode is selected through the pod sandbox annotation:

- `mksandbox.io/kernel-boot-mode=cold_boot`
- `mksandbox.io/kernel-boot-mode=snapshot`

If the annotation is omitted, `cold_boot` is used.

## Next Steps

1. Finish mature sub-kernel shutdown and kernel resource reuse.
2. Finish interactive container support:
   - `ExecSync`
   - `Attach`
   - additional compatibility hardening for the current TTY exec front-end
3. Continue hardening long-running container behavior and cleanup semantics.
4. Improve image service completeness.

## Kubelet Integration Example

Run kubelet with:

```bash
--container-runtime-endpoint=unix:///tmp/mkcri.sock
--image-service-endpoint=unix:///tmp/mkcri.sock
```

For production wiring details, see:

- `docs/architecture.md`
- `docs/k8s-integration.md`
- `docs/control-chain-mkring.md`
- `docs/networking.md`
