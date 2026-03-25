# mkring-bridge

`mkring-bridge` is the host-side control-plane bridge. It translates local `mkcri`
HTTP/Unix-socket requests into `mkring` control messages sent to a target
sub-kernel.

The tree currently provides:

- a local HTTP API for `mkcri`,
- host -> guest control message definitions,
- transport abstractions for control-plane round trips,
- a host-side device transport backed by `/dev/mkring_container_bridge`,
- support for TTY exec control operations (`prepare/start/resize/close`).

The matching kernel-side modules live under `mkring/`:

- `mkring_container_bridge.c`
- `mkring_stream_bridge.c`

## External API

Aligned with `mk-container/pkg/agent/mkring_bridge_client.go`:

- `POST /v1/kernels/{peerID}/wait-ready`
- `POST /v1/kernels/{peerID}/containers`
- `POST /v1/kernels/{peerID}/containers/{containerID}/start`
- `POST /v1/kernels/{peerID}/containers/{containerID}/stop`
- `POST /v1/kernels/{peerID}/containers/{containerID}/remove`
- `POST /v1/kernels/{peerID}/containers/{containerID}/status`
- `POST /v1/kernels/{peerID}/containers/{containerID}/read-log`
- `POST /v1/kernels/{peerID}/containers/{containerID}/exec-tty-prepare`
- `POST /v1/kernels/{peerID}/sessions/{sessionID}/start`
- `POST /v1/kernels/{peerID}/sessions/{sessionID}/resize`
- `POST /v1/kernels/{peerID}/sessions/{sessionID}/close`

## Control Message Model

Logical message unit: `internal/protocol.Envelope`

- `id`: request id
- `kind`: `request` / `response`
- `operation`: container control or TTY exec control operation
- `peer_kernel_id`: target sub-kernel `mkring.kernel_id`
- `kernel_id`: opaque kernel id used inside `mk-container`
- `payload`: operation-specific parameters
- `error`: structured error body

The bridge converts these logical operations into kernel bridge calls on
`/dev/mkring_container_bridge`.

## Running

```bash
cd mkring-bridge
go test ./...
go run ./cmd/mkring-bridge
```

By default it listens on:

- Unix socket: `/run/mk-container/mkring-bridge.sock`
- transport driver: `stub`

For the real host path, configure the device transport and point it at
`/dev/mkring_container_bridge`.

## Current Status

What is implemented:

- host-side control-plane bridge for the container lifecycle,
- wait-ready support,
- synchronous control round trips through the kernel bridge,
- status and log-read operations,
- TTY exec control-plane requests.

What is still separate from this binary:

- the stream data-plane itself,
- the CRI-facing TTY streaming endpoint in `mk-container/pkg/streaming`.

## Next Steps

1. Keep hardening error mapping and timeout behavior.
2. Continue simplifying the bridge/control protocol now that the TTY exec path is working end to end.
3. Add more transport and integration tests for exec session control.
