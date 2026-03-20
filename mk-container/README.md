# mk-container (multikernel + Kubernetes CRI adapter)

`mk-container` provides a Kubernetes-compatible CRI endpoint for a multikernel system where each sub-kernel runs exactly one container.

This tree now supports an `mkring`-based control chain:

`mkcri -> host mkring-bridge -> mkring -> sub-kernel guest agent -> containerd`

## What is implemented

- CRI gRPC server (`RuntimeService` + `ImageService`) for kubelet integration.
- One-pod-sandbox -> one sub-kernel mapping.
- One-kernel -> one-container enforcement.
- Sub-kernel lifecycle abstraction (`kernel.Manager`) using external start/stop commands.
- Sub-kernel peer-kernel-id allocation for mkring control-plane addressing.
- Sub-kernel container runtime abstraction (`agent.Factory`) with:
  - a runnable mock backend,
  - an `mkring` bridge client backend for host-side control forwarding.
- Pod IP allocation (for CNI handoff and status report).

## Repo layout

- `cmd/mkcri`: mkcri daemon entrypoint.
- `pkg/cri`: CRI service handlers.
- `pkg/runtime`: pod/container lifecycle engine.
- `pkg/kernel`: sub-kernel lifecycle control.
- `pkg/agent`: per-kernel runtime proxy and mkring bridge client.
- `docs/`: architecture and networking details.

## Quick start

```bash
go get google.golang.org/grpc@v1.40.0
go get k8s.io/cri-api@v0.23.0
go mod tidy
go test ./...
go run ./cmd/mkcri
```

By default, mkcri listens on `unix:///tmp/mkcri.sock`.

## Environment variables

- `MKCRI_LISTEN_SOCKET` (default: `/tmp/mkcri.sock`)
- `MK_KERNEL_START_COMMAND` (optional: command for booting sub-kernel, receives `MK_KERNEL_ID` env)
- `MK_KERNEL_STOP_COMMAND` (optional: command for stopping sub-kernel, receives `MK_KERNEL_ID` env)
- `MK_KERNEL_ENDPOINT_TEMPLATE` (default: `unix:///run/mk-kernel/%s/containerd.sock`)
- `MK_CONTROL_TRANSPORT` (default: `mock`, supported: `mock`, `mkring`)
- `MK_MKRING_BRIDGE_SOCKET` (default: `/run/mk-container/mkring-bridge.sock`)
- `MK_KERNEL_PEER_ID_BASE` (default: `1`)
- `MK_KERNEL_PEER_ID_MAX` (default: `255`)
- `MK_POD_CIDR_BASE` (default: `10.240.0.0`)
- `MK_POD_CIDR_MASK` (default: `24`)
- `MK_RUNTIME_NAME` (default: `mkcri`)
- `MK_RUNTIME_VERSION` (default: `0.1.0`)

When `MK_CONTROL_TRANSPORT=mkring`, `mkcri` allocates a numeric peer-kernel-id
for each sub-kernel and exports it to start/stop commands as
`MK_KERNEL_PEER_ID`. Boot scripts should wire that value to the guest kernel's
`mkring.kernel_id`.

## Kubelet integration (example)

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
