# MKSandbox

_A Multi-Kernel Sandbox Runtime_

## 1. 项目简介

MKSandbox 是一套面向 multikernel/sub-kernel 场景的 sandbox runtime 原型。它的目标是：

- 在 host 上提供一个兼容 CRI 的运行时入口
- 将容器控制请求从 host 转发到 guest/sub-kernel
- 在 guest 内使用 `containerd/runc` 真实启动容器
- 实现状态同步与日志回传

当前已经打通的主链包括：

- `RunPodSandbox`
- `CreateContainer`
- `StartContainer`
- guest `READY -> host`
- guest `EXITED -> host status`
- guest `stdout/stderr -> host LogPath`

当前控制链路如下：

```text
crictl
  -> mkcri
  -> mkring-bridge
  -> /dev/mkring_container_bridge (host)
  -> mkring
  -> /dev/mkring_container_bridge (guest)
  -> mk-guest-agent
  -> containerd / runc
  -> container process
```

## 2. 仓库目录说明

顶层目录包括：

- `mk-container`
- `mkring-bridge`
- `mk-guest-agent`
- `mkring`
- `scripts`

下面分别介绍各目录的编译方式和组件职责。

---

## 3. 各组件编译与功能说明

### 3.1 `mk-container`

#### 功能

`mk-container` 提供 host 侧 CRI 运行时实现，核心二进制是 `mkcri`。

主要职责：

- 对外提供 CRI gRPC 接口
- 管理 `PodSandbox -> sub-kernel` 映射
- 管理 `container -> guest runtime` 生命周期
- 调用 `mkring-bridge` 向 guest 转发控制请求
- 在 host 侧维护 pod/container 状态
- 同步 guest 状态与日志到 host

主要子目录：

- `cmd/mkcri`: `mkcri` 入口
- `pkg/cri`: CRI handler
- `pkg/runtime`: pod/container 生命周期编排
- `pkg/kernel`: sub-kernel 启停抽象
- `pkg/agent`: 连接 `mkring-bridge` 的 host 侧 client
- `docs`: 架构与集成文档

#### 编译方法

```bash
cd mk-container
go env -w GOPROXY=https://goproxy.cn,direct
go env -w GOSUMDB=sum.golang.google.cn
go get google.golang.org/grpc@v1.40.0
go get k8s.io/cri-api@v0.23.0
go mod tidy
go build -o mkcri ./cmd/mkcri
```

#### 测试

```bash
cd mk-container
go test ./pkg/agent ./pkg/runtime
```

---

### 3.2 `mkring-bridge`

#### 功能

`mkring-bridge` 是 host 侧用户态桥接进程，负责把 `mkcri` 的请求转成对
`/dev/mkring_container_bridge` 的调用，再通过 `mkring` 发往 guest。

主要职责：

- 提供本地 Unix socket / HTTP API
- 把 container control request 编码成 mkring container message
- 调用 host 内核 bridge 的 `WAIT_READY / CALL`
- 接收 guest response 并返回给 `mkcri`
- 提供 `status`、`read-log` 等同步查询能力

当前主要 API：

- `POST /v1/kernels/{peerID}/wait-ready`
- `POST /v1/kernels/{peerID}/containers`
- `POST /v1/kernels/{peerID}/containers/{containerID}/start`
- `POST /v1/kernels/{peerID}/containers/{containerID}/stop`
- `POST /v1/kernels/{peerID}/containers/{containerID}/remove`
- `POST /v1/kernels/{peerID}/containers/{containerID}/status`
- `POST /v1/kernels/{peerID}/containers/{containerID}/read-log`

#### 编译方法

```bash
cd mkring-bridge
go build -o mkring-bridge ./cmd/mkring-bridge
```

#### 测试

```bash
cd mkring-bridge
go test ./internal/...
```

---

### 3.3 `mk-guest-agent`

#### 功能

`mk-guest-agent` 是 guest/sub-kernel 内的用户态控制代理。

主要职责：

- 打开 guest 侧 `/dev/mkring_container_bridge`
- 启动时向 host 发送 `SET_READY`
- 接收 host 发来的容器控制请求
- 调用 guest 本地 runtime
- 当前通过 `ctr/containerd` 驱动 `containerd.sock`
- 维护 guest 本地 `.state`
- 维护 guest 本地 `.cri.log`
- 通过 `STATUS` 和 `READ_LOG` 向 host 返回状态与日志

主要源码：

- `src/agent.c`: 请求分发
- `src/transport_device.c`: guest 侧 device transport
- `src/runtime_containerd_stub.c`: 当前 containerd 适配
- `src/runtime_memory.c`: 内存版 stub runtime
- `src/protocol.c`: guest 内部协议

#### 编译方法

```bash
cd mk-guest-agent
make STATIC=1
```

如果不需要静态链接，也可以：

```bash
cd mk-guest-agent
make
```

#### 测试

```bash
cd mk-guest-agent
make test
```

---

### 3.4 `mkring`

#### 功能

`mkring` 是 multikernel 场景下的内核态通信基础设施。

主要职责：

- 通过共享内存组织 `kernel -> kernel` 消息队列
- 通过 IPI 在 kernel 间通知消息到达
- 提供 `mkring_send()` / `mkring_register_rx_cb()` 等 API
- 提供 container 控制面协议 UAPI：
  - `mkring_container.h`
- 提供用户态桥接模块：
  - `mkring_container_bridge.c`

其中：

- `mkring.c` / `init_mk.c` 是内核内置部分
- `mkring_container_bridge.c` / `mkring_test.c` 是模块部分

#### 编译方法

`mkring` 不是普通用户态工程，需要通过 Linux 内核构建系统编译。

如果你在 Linux 内核源码树中集成该目录，常见方式有两种：

##### 方式 A：作为内核树的一部分构建

在内核源码根目录执行：

```bash
make -j$(nproc)
make modules -j$(nproc)
```

##### 方式 B：单独构建该目录下的模块

在内核源码根目录执行：

```bash
make M=/path/to/mkring modules
```

例如：

```bash
make M=/path/to/mkring modules
```

安装模块：

```bash
make modules_install
depmod -a
```

#### 运行时加载

host：

```bash
modprobe mkring_container_bridge role=host device_name=mkring_container_bridge
```

guest：

```bash
modprobe mkring_container_bridge role=guest device_name=mkring_container_bridge
```

---

### 3.5 `scripts`

#### 功能

`scripts` 目录提供 bring-up 和运行编排脚本，不需要编译。

主要包括：

- `start-host.sh`
  - 启动 host 侧控制面
  - 拉起 `mkring-bridge`
  - 拉起 `mkcri`
  - 检查 `/dev/mkring_container_bridge`
- `init`
  - guest init 脚本
  - 加载内核模块
  - 启动 `containerd`
  - 启动 `mk-guest-agent`
  - 导入镜像

#### 使用方式

host：

```bash
./scripts/start-host.sh
```

guest：

- `scripts/init` 被作为 guest initrd 内的 `/init` 使用

---

## 4. 当前系统能力总结

目前项目已经具备以下能力：

1. host 通过 CRI 启动 guest/sub-kernel
2. guest 通过 `SET_READY` 向 host 完成初始化同步
3. host 在 guest 中创建 container
4. host 在 guest 中启动 container
5. guest 容器退出后，host 状态同步为 `CONTAINER_EXITED`
6. guest 容器 stdout/stderr 会先写入 guest `.cri.log`
7. host 通过 `READ_LOG` 拉取并写入 `LogPath`
8. host monitor loop 可以自动同步日志与状态
9. 查询时同步逻辑保留为 fallback

---

## 5. 未来开发计划

1. 正常的 `kernel stop` 与资源回收
2. 补齐 pod IP、peer kernel id 等资源释放
3. 将状态同步、日志同步、cleanup 串成完整生命周期状态机
4. 使用长运行容器验证 monitor loop 的持续同步效果
5. 完善日志链的稳定性与保留策略
6. 补充 host/guest 重启后的状态与日志恢复能力
7. 完整实现 CRI ImageService
8. 优化镜像/rootfs 管理机制
9. 完善 CRI 展示信息与元数据
10. 后续再考虑 `logs/exec/attach/port-forward` 等高级接口

---

## 6. 典型运行流程

### host 启动

```bash
./scripts/start-host.sh
```

### 基本测试

```bash
crictl runp /tmp/pod-config.json
crictl create <pod-id> /tmp/container-config.json /tmp/pod-config.json
crictl start <container-id>
crictl inspect <container-id>
crictl ps -a
```

### 查看 host 日志落盘

```bash
sed -n '1,20p' /tmp/mkpod/file.log
```

---

## 7. 备注

当前仓库是一个多组件原型工程，既包含：

- Go 用户态组件
- C 用户态组件
- Linux 内核代码
- guest/host bring-up 脚本

因此部署时需要注意：

- host 与 guest 的 `mkring_container_bridge.ko` 要保持一致
- `mkring-bridge`、`mkcri`、`mk-guest-agent` 要与当前协议版本一致
- guest initrd 需要重新打包更新后的 `mk-guest-agent` 与内核模块
