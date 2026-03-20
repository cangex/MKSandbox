# mk-guest-agent

`mk-guest-agent` 现在是一个 C 语言实现的 sub-kernel 用户态控制代理骨架。

它保留了之前的分层设计，但替换成了标准 C 工程：

- `include/`: 对外头文件
- `src/`: 协议、transport、runtime、dispatcher、main
- `tests/`: 基于 stub transport 的集成测试
- `Makefile`: 构建和测试入口

## 处理流程

```text
host mkring-bridge
  -> guest-side mkring bridge
  -> mkga_transport_receive()
  -> mkga_agent_handle()
  -> mkga_runtime_*
  -> mkga_transport_send()
```

## 当前实现

- `src/agent.c`
  负责请求分发，处理 `create/start/stop/remove container`
- `src/transport_stub.c`
  提供一个基于 pthread 条件变量的 stub transport，方便本地联调
- `src/transport_device.c`
  直接对接 `/dev/mkring_container_bridge`，在启动时发
  `MKRING_CONTAINER_IOC_SET_READY`，运行时通过 `read()/write()` 转发
  `REQUEST/RESPONSE`
- `src/runtime_memory.c`
  提供一个内存版 runtime，实现最小容器状态机
- `src/runtime_containerd_stub.c`
  提供第一版 guest-agent -> containerd 适配，当前通过
  `fork/exec ctr --address ...` 驱动本地 `containerd.sock`
- `src/protocol.c`
  定义控制消息、错误体和响应构造 helper

## 和 mkring 内核 bridge 的对接方式

`mk-guest-agent` 现在在 `include/mkring_container.h` 内自带一份
guest 用户态需要的 `mkring-container` ABI 定义，不再直接引用源码树里的
`mkring_container.h`。目标接法是：

1. guest kernel 加载 `mkring_container_bridge.ko role=guest`
2. `mk-guest-agent` 以 `mkring-device` transport 打开 `/dev/mkring_container_bridge`
3. `containerd` runtime 在初始化时通过 `ctr version` 等待本地
   `containerd.sock` 可用
4. transport 创建后发 `MKRING_CONTAINER_IOC_SET_READY`
5. agent 通过 `read()` 收到 host 发来的 `REQUEST`
6. agent 调本地 runtime
7. agent 通过 `write()` 把 `RESPONSE` 写回 kernel bridge

## 构建

```bash
cd mk-guest-agent
make
make test
```

## 环境变量

- `MK_GUEST_AGENT_TRANSPORT`
  默认 `stub`，真实链路使用 `mkring-device`
- `MK_GUEST_AGENT_RUNTIME`
  默认 `memory`，可选 `containerd`
- `MK_GUEST_AGENT_CONTAINERD_SOCKET`
  默认 `/run/containerd/containerd.sock`
- `MK_GUEST_AGENT_CTR_PATH`
  默认 `ctr`
- `MK_GUEST_AGENT_CONTAINERD_NAMESPACE`
  默认 `mk`
- `MK_GUEST_AGENT_CONTAINERD_TIMEOUT_MS`
  默认 `5000`
- `MK_GUEST_AGENT_BRIDGE_DEVICE`
  默认 `/dev/mkring_container_bridge`
- `MK_GUEST_AGENT_PEER_KERNEL_ID`
  默认 `0`，表示 host 在 mkring 里的 kernel id
- `MK_GUEST_AGENT_INBOUND_BUFFER`
  默认 `32`
- `MK_GUEST_AGENT_RECEIVE_TIMEOUT_MS`
  默认 `200`

## 下一步

1. 把 `src/runtime_containerd_stub.c` 从 `ctr` 桥接升级成真实 containerd client
2. 补更完整的 image / task / exit-code 错误映射
3. 给 `ctr` 路线补单元测试和真实 guest 环境联调
