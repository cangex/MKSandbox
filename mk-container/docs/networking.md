# Container Inter-Communication in Multikernel Mode

## Target

Enable pod-to-pod communication while pods are hosted in different hardware-isolated sub-kernels.

## Data path

1. Host root kernel creates a Linux bridge (for example `mkbr0`).
2. For each sub-kernel pod:
- create a dedicated L2 link to that sub-kernel (tap/vhost-vsock/virtio-net based on platform),
- attach host side to `mkbr0`,
- configure pod IP route in sub-kernel namespace.
3. CNI assigns pod IP and route policy.
4. kube-proxy / Cilium / Calico handles service-level forwarding as usual.

## Recommended implementation split

- `mkcri`:
  - manages pod lifecycle and IP reservation,
  - calls network setup hooks during `RunPodSandbox` / teardown during `RemovePodSandbox`.

- `mknetd` (recommended sidecar daemon):
  - owns privileged netlink operations,
  - sets up bridge, veth/tap, tc/qos rules,
  - returns allocated link metadata back to mkcri.

This split keeps CRI server logic simpler and reduces blast radius.

## Minimal API contract (mkcri -> mknetd)

- `SetupPodNetwork(podID, kernelID, podIP, mtu) -> {ifNameHost, ifNameGuest, gateway}`
- `TeardownPodNetwork(podID, kernelID)`

## K8s compatibility notes

- Pod IP must be stable for the lifetime of sandbox.
- `PodSandboxStatus.Network.IP` should reflect real dataplane IP.
- DNS and Service CIDR routes must exist inside sub-kernel rootfs network stack.
