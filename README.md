# MKSandbox

_A Multi-Kernel Sandbox Runtime Prototype_

## 1. Overview

MKSandbox is a prototype sandbox runtime for multikernel and sub-kernel environments.
Its goals are:

- expose a CRI-compatible runtime endpoint on the host,
- forward container control requests from the host into a guest/sub-kernel,
- run real containers inside the guest with `containerd` and `runc`,
- synchronize lifecycle state and logs back to the host.

The main path that is working today includes:

- `RunPodSandbox`
- `CreateContainer`
- `StartContainer`
- `ContainerConfig.command/args -> guest ctr`
- guest `READY -> host`
- guest `EXITED -> host status`
- guest `stdout/stderr -> host LogPath`
- continuous log sync for long-running containers
- manual `StopContainer`
- TTY `Exec` over the `mkring` transport path
- `crictl exec -it` through the CRI/SPDY front-end

The current control chain is:

```text
crictl
  -> mkcri
  -> userspace control transport over sys_mkring_transport
  -> sys_mkring_transport (host)
  -> mkring
  -> sys_mkring_transport (guest)
  -> mk-guest-agent
  -> containerd / runc
  -> container process
```

The kernel-side control path is intentionally small:

- the kernel only provides transport semantics (`SEND` / `RECV`) over `mkring`,
- channel-1 message meaning stays in userspace,
- channel-3 stream packet meaning also stays in userspace,
- host userspace owns peer `READY` tracking and request/response matching,
- guest userspace owns `READY` publication plus request decode / response encode,
- channel 2 (`MKRING_CHANNEL_SYSTEM`) remains reserved and unused today.

The current TTY exec data path is:

```text
Exec RPC
  -> mkcri streaming server
  -> host userspace stream transport over sys_mkring_transport
  -> sys_mkring_transport (host, channel 3)
  -> mkring
  -> sys_mkring_transport (guest, channel 3)
  -> guest userspace stream transport
  -> mk-guest-agent PTY session
  -> ctr tasks exec --tty
```

## 2. Repository Layout

Top-level directories:

- `mk-container`
- `mk-guest-agent`
- `mkring`
- `scripts`

The sections below summarize the role and build instructions for each component.

## 3. Components

### 3.1 `mk-container`

#### Responsibility

`mk-container` provides the host-side CRI runtime implementation. Its main binary is `mkcri`.

Main responsibilities:

- expose the CRI gRPC API,
- manage the `PodSandbox -> sub-kernel` mapping,
- manage `container -> guest runtime` lifecycle operations,
- issue `mkring` control calls directly from the host runtime path,
- issue exec stream packets directly from the host runtime path,
- maintain host-side pod/container state,
- synchronize guest state and logs back to the host,
- host the CRI-facing TTY exec streaming endpoint, including the current SPDY front-end used by `crictl exec -it`.

Main subdirectories:

- `cmd/mkcri`: mkcri entrypoint
- `cmd/mkexecurl`: helper for requesting an `Exec` URL during bring-up
- `pkg/cri`: CRI handlers
- `pkg/runtime`: pod/container lifecycle orchestration
- `pkg/kernel`: sub-kernel start/stop abstraction
- `pkg/agent`: host-side per-kernel runtime clients, including the direct `mkring` path
- `pkg/streaming`: host-side TTY exec streaming server and transport data plane
- `docs`: architecture and integration documents

#### Build

```bash
cd mk-container
go env -w GOPROXY=https://goproxy.cn,direct
go env -w GOSUMDB=sum.golang.google.cn
go get google.golang.org/grpc@v1.40.0
go get k8s.io/cri-api@v0.23.0
go mod tidy
go build -o mkcri ./cmd/mkcri
go build -o mkexecurl ./cmd/mkexecurl
```

#### Test

```bash
cd mk-container
go test ./pkg/agent ./pkg/runtime ./pkg/streaming
```

### 3.2 `mk-guest-agent`

#### Responsibility

`mk-guest-agent` is the guest/sub-kernel user-space control agent.

Main responsibilities:

- publish a channel-1 `READY` message to the host after startup,
- receive container control requests from the host,
- call the local guest runtime,
- drive `containerd` through `ctr` today,
- maintain guest-local `.state` and `.cri.log`,
- return status and logs through `STATUS` and `READ_LOG`,
- host guest-side PTY exec sessions over the channel-3 transport path.

Main source files:

- `src/agent.c`: request dispatch
- `src/transport_mkring.c`: guest-side direct-entry control transport
- `src/stream_device.c`: guest-side stream data plane
- `src/runtime_containerd_stub.c`: current `containerd` adapter
- `src/session.c`: TTY exec session state
- `src/runtime_memory.c`: in-memory stub runtime
- `src/protocol.c`: guest internal protocol helpers

#### Build

```bash
cd mk-guest-agent
make STATIC=1
```

If static linking is not required:

```bash
cd mk-guest-agent
make
```

#### Test

```bash
cd mk-guest-agent
make test
```

### 3.3 `mkring`

#### Responsibility

`mkring` is the in-kernel communication substrate for the multikernel environment.

Main responsibilities:

- organize `kernel -> kernel` queues in shared memory,
- notify peer kernels through IPI,
- expose APIs such as `mkring_send()` and `mkring_register_rx_cb()`,
- provide the control-plane protocol in `mkring_container.h`,
- provide the direct-entry control UAPI in `mkring_transport_uapi.h`,
- provide the stream data-plane UAPI in `mkring_stream.h`,
- provide the built-in transport substrate used by both control and stream channels.

In practice:

- `mkring.c` and `init_mk.c` are built-in kernel components,
- `mkring_test.c` remains a module.

#### Build

`mkring` is kernel code, not a normal user-space project. Build it through the Linux kernel build system.

##### Option A: Build as part of the kernel tree

```bash
make -j$(nproc)
make modules -j$(nproc)
```

##### Option B: Build the directory as external modules

```bash
make M=/path/to/mkring modules
```

Example:

```bash
make M=/path/to/mkring modules
```

Install modules:

```bash
make modules_install
depmod -a
```

### 3.5 `scripts`

#### Responsibility

The `scripts` directory contains bring-up and runtime orchestration helpers. It does not need compilation.

Main scripts:

- `start-host.sh`
  - starts the host control plane,
  - starts `mkcri`,
  - exports the `sys_mkring_transport` syscall number to `mkcri`.
- `init`
  - guest init script used as `/init` inside the guest initrd,
  - loads kernel modules,
  - mounts `devpts`,
  - starts `containerd`,
  - starts `mk-guest-agent`,
  - auto-imports all image tarballs under `/images`.
- `run-hello-world.sh`
  - runs a short-lived container,
  - good for validating exit behavior, log sync, and auto cleanup.
- `run-long-log-container.sh`
  - runs a long-lived container that continuously writes logs,
  - good for validating `command/args`, continuous log sync, and manual stop.
- `run-tty-exec-smoke.sh`
  - runs the raw HTTP TTY exec smoke test used during bring-up.
- `run-exec-it-container.sh`
  - prepares a long-running shell container for interactive `crictl exec -it` validation.

#### Usage

Host:

```bash
./scripts/start-host.sh
```

Guest:

- `scripts/init` is packaged into the guest initrd as `/init`

## 4. Current Capabilities

The project currently supports:

1. the host can boot a guest/sub-kernel through CRI,
2. the guest completes initialization sync through a channel-1 `READY` message,
3. the host can create containers inside the guest,
4. the host can start containers inside the guest,
5. `ContainerConfig.command/args` are passed through to guest `ctr`,
6. guest container exit is synchronized back to the host as `CONTAINER_EXITED`,
7. guest `stdout/stderr` is written into guest `.cri.log`,
8. the host pulls guest logs through `READ_LOG` into the host `LogPath`,
9. the host monitor loop keeps long-running status and logs synchronized,
10. `EXITED && log EOF` can trigger pod-level auto cleanup,
11. manual `StopContainer` now coexists with auto cleanup,
12. TTY `Exec` works through the current `mkring` stream data plane.
13. `crictl exec -it` works through the CRI/SPDY front-end.

### Current Boundaries

Sub-kernel shutdown is not yet a mature resource-reuse solution:

- `mkhalt` / `StopPod()` can shut the guest down,
- but kernel-level resource reclaim across host and guest is still incomplete,
- especially CPU reclaim, instance-slot reuse, kimage cleanup, and other host-side teardown semantics.

So shutdown is good enough today for:

- pod-level cleanup after container exit,
- validating the guest shutdown chain.

It should **not** yet be treated as a finished kernel resource reuse mechanism.

The interactive container path is also not in its final product form yet:

- `crictl exec -it` is now working for TTY exec,
- the stream data plane and PTY-backed guest exec path are in place,
- `ExecSync` and `Attach` are still pending,
- exit/error polish and additional compatibility hardening are still in progress.

## 5. Development Roadmap

1. Finish mature multikernel sub-kernel shutdown and kernel-level resource reuse.
2. Harden host-side reclaim semantics for CPU, instance slots, and kimage teardown.
3. Continue integrating lifecycle state sync, log sync, and cleanup into a stricter state machine.
4. Harden long-running container logging and retention behavior.
5. Add restart/recovery behavior for host and guest restarts.
6. Finish interactive container support:
   - `ExecSync`
   - `Attach`
   - further hardening of the CRI-compatible TTY exec front-end
7. Finish the CRI `ImageService` implementation.
8. Improve image and rootfs management.
9. Improve CRI-facing status and metadata fidelity.

## 6. Typical Workflow

### Start the host side

```bash
./scripts/start-host.sh
```

### Basic control path test

```bash
crictl runp /tmp/pod-config.json
crictl create <pod-id> /tmp/container-config.json /tmp/pod-config.json
crictl start <container-id>
crictl inspect <container-id>
crictl ps -a
```

### Check host log output

```bash
sed -n '1,20p' /tmp/mkpod/file.log
```

### Raw TTY exec smoke test

```bash
./scripts/run-tty-exec-smoke.sh <container-id>
```

## 7. Notes

This repository is a multi-component prototype containing:

- Go user-space components,
- C user-space components,
- Linux kernel code,
- guest/host bring-up scripts.

When deploying, keep these pieces aligned:

- the running kernel's `sys_mkring_transport` syscall number exported to `mkcri`,
- `mkcri` and `mk-guest-agent` matching the current protocol version,
- guest initrd repacked with the updated guest agent.
