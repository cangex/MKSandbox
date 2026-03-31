# mk-guest-agent

`mk-guest-agent` is the guest/sub-kernel user-space control agent, implemented in C.

It keeps the original layered design while using a conventional C project layout:

- `include/`: public headers and guest-side mirrored UAPI headers
- `src/`: protocol, transport, runtime, session manager, dispatcher, and main
- `tests/`: integration-style tests built on the stub transport
- `Makefile`: build and test entrypoint

## Request Handling Flow

```text
host userspace control transport
  -> guest-side direct mkring syscall backend
  -> mkga_transport_receive()
  -> mkga_agent_handle()
  -> mkga_runtime_*
  -> mkga_transport_send()
```

## Current Implementation

- `src/agent.c`
  - request dispatch for create/start/stop/remove/status/read-log and TTY exec control operations
- `src/transport_stub.c`
  - pthread-based stub transport for local debugging
- `src/transport_mkring.c`
  - control-plane transport built on `sys_mkring_transport`
  - publishes a `READY` message at startup
  - receives channel-1 `REQUEST` messages through the direct transport entry
  - sends channel-1 `RESPONSE` messages back through the direct transport entry
- `src/stream_device.c`
  - data-plane stream transport for `/dev/mkring_stream_bridge`
  - handles TTY stdin/output/exit frames
- `src/runtime_memory.c`
  - in-memory runtime with a minimal container state machine
- `src/runtime_containerd_stub.c`
  - current guest runtime adapter backed by `fork/exec ctr --address ...`
  - includes long-running container status/log support and PTY-backed TTY exec support
- `src/session.c`
  - guest-side TTY exec session table and stdin handling
- `src/protocol.c`
  - control-message helpers, error payloads, and response builders

## How It Integrates With the mkring Transport Layer

`mk-guest-agent` carries guest-side mirrored protocol headers for:

- `include/mkring_container.h`
- `include/mkring_stream.h`

The direct transport UAPI comes from the shared header:

- [mkring_transport_uapi.h](../mkring/mkring_transport_uapi.h)

The expected wiring is:

1. guest kernel exposes `sys_mkring_transport`
2. guest kernel loads `mkring_stream_bridge.ko`
3. `mk-guest-agent` starts with `mkring` transport
4. the `containerd` runtime path waits for local `containerd.sock` to become ready
5. transport creation sends a channel-1 `READY` message
6. the agent receives host container `REQUEST` messages through `sys_mkring_transport`
7. the agent calls the local runtime
8. the agent sends container `RESPONSE` messages back through `sys_mkring_transport`
9. TTY exec traffic flows through `/dev/mkring_stream_bridge`

The important boundary is:

- the kernel syscall only moves opaque messages on a channel,
- `mk-guest-agent` owns container-message validation and business handling,
- channel 2 remains reserved and is not used by the guest today.

## Build

```bash
cd mk-guest-agent
make
make test
```

For static linking:

```bash
cd mk-guest-agent
make STATIC=1
```

## Environment Variables

- `MK_GUEST_AGENT_TRANSPORT`
  - default: `stub`
  - real control path: `mkring`
- `MK_GUEST_AGENT_RUNTIME`
  - default: `memory`
  - real runtime: `containerd`
- `MK_GUEST_AGENT_CONTAINERD_SOCKET`
  - default: `/run/containerd/containerd.sock`
- `MK_GUEST_AGENT_CTR_PATH`
  - default: `ctr`
- `MK_GUEST_AGENT_CONTAINERD_NAMESPACE`
  - default: `mk`
- `MK_GUEST_AGENT_CONTAINERD_TIMEOUT_MS`
  - default: `5000`
- `MK_GUEST_AGENT_STREAM_DEVICE`
  - default: `/dev/mkring_stream_bridge`
- `MK_GUEST_AGENT_PEER_KERNEL_ID`
  - default: `0`
  - the host kernel id in the `mkring` topology
- `MK_GUEST_AGENT_INBOUND_BUFFER`
  - default: `32`
- `MK_GUEST_AGENT_RECEIVE_TIMEOUT_MS`
  - default: `200`

## Current Status

Today the guest agent supports:

- host-driven create/start/stop/remove/status/read-log,
- containerd-backed long-running containers,
- command/args passthrough,
- guest-side CRI log generation,
- TTY exec backed by PTY sessions and `/dev/mkring_stream_bridge`,
- the guest-side backend used by `crictl exec -it`.

What is not finished yet:

- `ExecSync`,
- `Attach`,
- a native containerd client implementation that replaces the current `ctr` shell-out path.

## Next Steps

1. Replace more `ctr` shell-out logic with a more direct runtime integration where it is worth the complexity.
2. Harden image/task/exit-code error mapping.
3. Expand tests for TTY exec and stream data-plane behavior.
4. Add `ExecSync` and `Attach` support.
