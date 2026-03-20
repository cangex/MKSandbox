# Architecture

## Goal

Use multikernel isolation as the container boundary:

- each Kubernetes pod sandbox maps to one sub-kernel,
- each sub-kernel runs one container,
- kubelet talks standard CRI to mkcri.

## Components

1. `kubelet`
- Calls CRI runtime/image APIs.

2. `mkcri` (this project)
- Implements CRI server.
- Schedules pod sandbox to sub-kernel.
- Proxies container lifecycle calls to a per-kernel runtime agent.

3. `kernel.Manager`
- Starts/stops sub-kernels through hardware partition control commands.
- Assigns a numeric peer-kernel-id used by `mkring` routing.

4. `agent.Client`
- Talks to either:
  - the mock backend, or
  - a host-side `mkring` bridge that forwards requests into the sub-kernel.

5. `sub-kernel`
- Has dedicated CPU/devices (hard partition).
- Own rootfs includes containerd/runc and a guest control agent.

6. `mkring`
- Provides the host <-> sub-kernel kernel messaging path.
- Carries control-plane requests through a host bridge / guest agent pair.

## Lifecycle mapping

- `RunPodSandbox`:
  - allocate a sub-kernel,
  - allocate a peer-kernel-id for mkring,
  - boot kernel via `kernel.Manager.StartKernel`,
  - assign pod IP,
  - return sandbox ID.

- `CreateContainer`:
  - route to the selected sub-kernel via the configured control transport,
  - enforce one container per kernel.

- `StartContainer/StopContainer/RemoveContainer`:
  - proxy to sub-kernel runtime,
  - update CRI-visible state.

- `StopPodSandbox/RemovePodSandbox`:
  - stop container if needed,
  - stop and release sub-kernel.

## Why CRI-first design

- no kubelet fork required,
- native support for RuntimeClass and standard K8s scheduling/control plane,
- easy staged rollout: swap container runtime endpoint only.
