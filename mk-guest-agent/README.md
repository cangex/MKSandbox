# mk-guest-agent

`mk-guest-agent` is the guest/sub-kernel user-space control agent, implemented in C.

It keeps the original layered design while using a conventional C project layout:

- `include/`: public headers and guest-side mirrored UAPI headers
- `src/`: protocol, transport, runtime, session manager, dispatcher, and main
- `tests/`: integration-style tests built on the stub transport
- `Makefile`: build and test entrypoint

## Request Handling Flow

```text
host mkring-bridge
  -> guest-side mkring bridge
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
- `src/transport_device.c`
  - control-plane device transport for `/dev/mkring_container_bridge`
  - sends `MKRING_CONTAINER_IOC_SET_READY` at startup
  - forwards `REQUEST/RESPONSE` through `read()` / `write()`
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

## How It Integrates With the mkring Kernel Bridges

`mk-guest-agent` carries guest-side mirrored ABI definitions for:

- `include/mkring_container.h`
- `include/mkring_stream.h`

instead of including kernel-tree headers directly.

The expected wiring is:

1. guest kernel loads `mkring_container_bridge.ko role=guest`
2. guest kernel loads `mkring_stream_bridge.ko`
3. `mk-guest-agent` starts with `mkring-device` transport on `/dev/mkring_container_bridge`
4. the `containerd` runtime path waits for local `containerd.sock` to become ready
5. transport creation sends `MKRING_CONTAINER_IOC_SET_READY`
6. the agent receives host `REQUEST`s through `read()`
7. the agent calls the local runtime
8. the agent writes `RESPONSE`s back through the kernel bridge
9. TTY exec traffic flows through `/dev/mkring_stream_bridge`

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
  - real control path: `mkring-device`
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
- `MK_GUEST_AGENT_BRIDGE_DEVICE`
  - default: `/dev/mkring_container_bridge`
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
