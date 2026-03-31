# Kubernetes Integration Guide

## 1) Run mkcri daemon

Example:

```bash
MKCRI_LISTEN_SOCKET=/var/run/mkcri.sock \
MK_KERNEL_START_COMMAND="/usr/local/bin/mk-kernelctl start" \
MK_KERNEL_STOP_COMMAND="/usr/local/bin/mk-kernelctl stop" \
MK_CONTROL_TRANSPORT=mkring \
MKCRI_TRANSPORT_SYSCALL_NR=<sys_mkring_transport_nr> \
go run ./cmd/mkcri
```

`mk-kernelctl` should read `MK_KERNEL_ID` and `MK_KERNEL_PEER_ID` from env and
start/stop the target sub-kernel. The guest kernel boot arguments should include
`mkring.kernel_id=$MK_KERNEL_PEER_ID`.

## 2) Point kubelet to mkcri

Set kubelet flags:

```bash
--container-runtime-endpoint=unix:///var/run/mkcri.sock
--image-service-endpoint=unix:///var/run/mkcri.sock
```

## 3) RuntimeClass for multikernel

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: multikernel
handler: multikernel
```

Pods can reference it:

```yaml
spec:
  runtimeClassName: multikernel
```

`handler` is delivered to CRI as `RuntimeHandler`; mkcri keeps it in pod sandbox state.

## 4) CNI and network readiness

mkcri reports pod IP and runtime/network ready status. Real networking should be provisioned by host CNI + sub-kernel link setup (see `docs/networking.md`).

## 5) Provide host transport + guest agent

Current `pkg/agent/mock_client.go` is for bring-up.
The default `mkring` control transport expects:

- a host `sys_mkring_transport` entry whose syscall number is exported to mkcri as `MKCRI_TRANSPORT_SYSCALL_NR`,
- a guest-side agent inside each sub-kernel,
- guest-agent -> containerd integration,
- request/response forwarding over `mkring`.
