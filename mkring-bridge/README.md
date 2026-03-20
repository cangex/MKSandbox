# mkring-bridge

`mkring-bridge` 是 host 侧的控制面桥接骨架，职责是把 `mkcri` 的本地
HTTP/Unix-socket 请求翻译成发往目标 sub-kernel 的 `mkring` 控制消息。

当前目录提供：

- 面向 `mkcri` 的本地 HTTP API
- host -> guest 的控制消息格式
- 支持分片的 frame 结构定义
- `Transport` 抽象，方便后续接用户态/内核态的 `mkring` 桥
- 一个 `stub` transport，便于先联调 HTTP 协议

内核侧现在已经补了一层配套模块：

- `mkring/mkring_container_bridge.c`
- device: `/dev/mkring_container_bridge`
- UAPI: `mkring/mkring_container.h`

## 对外 API

与 `mk-container/pkg/agent/mkring_bridge_client.go` 对齐：

- `POST /v1/kernels/{peerID}/wait-ready`
- `POST /v1/kernels/{peerID}/containers`
- `POST /v1/kernels/{peerID}/containers/{containerID}/start`
- `POST /v1/kernels/{peerID}/containers/{containerID}/stop`
- `POST /v1/kernels/{peerID}/containers/{containerID}/remove`

## 控制消息格式

逻辑消息单位：`internal/protocol.Envelope`

- `id`: 请求 ID
- `kind`: `request` / `response`
- `operation`: `create_container` / `start_container` / `stop_container` / `remove_container`
- `peer_kernel_id`: 目标 sub-kernel 的 `mkring.kernel_id`
- `kernel_id`: `mk-container` 内部的 opaque kernel ID
- `payload`: 具体操作参数
- `error`: 错误体

传输分片单位：`internal/protocol.Frame`

- `message_id`
- `sequence`
- `final`
- `payload`

这层是为了适配 `mkring` 的固定消息槽大小；后续真实实现只需要把
`Envelope <-> []Frame` 接到内核态桥接上即可。

## 运行

```bash
cd mkring-bridge
go test ./...
go run ./cmd/mkring-bridge
```

默认监听：

- Unix socket: `/run/mk-container/mkring-bridge.sock`
- transport driver: `stub`

## 下一步

1. 实现真正的 host device transport，把 `Transport.RoundTrip()` 接到 `MKRING_CONTAINER_IOC_CALL`
2. 在首次访问 peer 前补 `MKRING_CONTAINER_IOC_WAIT_READY`
3. 做 frame 分片、超时和重试策略
