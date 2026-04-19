# 使用 mknet 手动跑通 CloudSuite

这份文档说明当前 MKSandbox 手动接入 CloudSuite 的方法。目标是：CloudSuite 仍然使用普通 IP/TCP 通信，而 subkernel 之间的实际数据路径通过 `mknetproxy` 和 mkring 承载。

当前实现的边界要先说清楚：

```text
mkcri / mk-guest-agent 可以自动完成 pod IP 和路由配置。
mknetproxy 目前还不会被 guest-agent 自动启动。
因此，跑 CloudSuite 时仍然需要手动在参与通信的 guest 里启动 mknetproxy。
```

这套方式的重点不是最终自动化，而是先把 CloudSuite 的多容器通信路径跑通。

## 1. Subkernel 启动后 init 做了哪些网络初始化

subkernel 启动后，guest 里的 `/init` 只做最基础的网络准备。它不会自己决定最终 pod IP，也不会自己发现其他 subkernel 的 IP。

当前 `/init` 的主要启动顺序是：

```text
/init
  -> 挂载 /proc, /sys, /dev, /run, /tmp, cgroup2
  -> 创建设备节点
  -> 准备 runc 链接
  -> prepare_guest_network
  -> 检查 overlayfs
  -> 挂载 daxfs，如果可用
  -> 从 /daxfs/bin 复制二进制，如果存在
  -> 启动 containerd
  -> 从 /daxfs/images 或 /images 导入镜像
  -> 启动 mk-guest-agent
```

其中和 mknet 直接相关的是 `prepare_guest_network`：

```sh
ip link set lo up
```

也就是说，subkernel 启动后 init 只保证 `lo` 是 up 的。真正的 pod IP 是后面由 host 侧 `mkcri` 分配，并通过 mkring control path 下发给 guest-agent。

`/init` 还会查找这些工具：

```text
ip
iptables
mknetproxy
```

然后把路径传给 `mk-guest-agent`：

```text
MK_GUEST_AGENT_IP_PATH
MK_GUEST_AGENT_IPTABLES_PATH
MK_GUEST_AGENT_MKNET_PROXY_PATH
MK_GUEST_AGENT_MKNET_PROXY_PORT
MK_GUEST_AGENT_MKRING_SYSCALL_NR
```

当前 initrd/rootfs 里建议至少有：

```text
/init
/bin/mk-guest-agent
/bin/mknetproxy
/bin/ip 或 /sbin/ip
/bin/containerd
/bin/ctr
/bin/runc
```

`mknetproxy` 已经可以静态编译。用 `file mknetproxy` 检查时，应该能看到：

```text
statically linked
```

### ConfigureNetwork 什么时候发生

pod IP 不是 subkernel boot 时配置的，而是在 `CreateContainer` 阶段配置的。

当前链路是：

```text
RunPodSandbox
  -> mkcri 启动 subkernel
  -> mkcri 等待 mk-guest-agent READY
  -> mkcri 分配 pod IP
  -> mkcri 分配 peer kernel ID

CreateContainer
  -> mkcri 根据当前 pod 和已有 pods 构造 NetworkSpec
  -> mkcri 通过 mkring 发送 ConfigureNetwork
  -> mk-guest-agent 在 guest 内配置 lo 和 route
  -> mkcri 下发 container env
  -> mkcri 创建 container
```

所以，验证 `ConfigureNetwork` 是否成功，应该在 container 创建之后进入 guest 看：

```sh
ip addr show lo
ip route
```

成功时应该看到类似：

```text
inet 10.240.0.2/32 scope global lo
10.240.0.0/24 dev lo scope link
```

这说明：

```text
mkcri -> mkring -> mk-guest-agent -> ip addr / route
```

这条控制链已经走通。

## 2. 跑通 CloudSuite 还需要手动配置什么

CloudSuite 通常是多容器结构，比如 client/server、master/worker、frontend/backend。它们原本通过普通 IP 和端口互相连接。

在 MKSandbox 当前手动版里，需要你手动整理一张拓扑表：

```text
组件        角色       pod IP        peer kernel ID    监听端口    连接目标
server-0    server     10.240.0.2    1                 11211       -
client-0    client     10.240.0.3    2                 -           10.240.0.2:11211
```

多个 server 的例子：

```text
组件        角色       pod IP        peer kernel ID    监听端口
server-0    server     10.240.0.2    1                 11211
server-1    server     10.240.0.3    2                 11211
client-0    client     10.240.0.4    3                 -
```

当前最稳的启动顺序是：

```text
先启动 server/slave pod
再启动 client/master pod
```

原因是：当前 `ConfigureNetwork` 只在当前 pod 的 `CreateContainer` 阶段下发。后启动的 client 能看到先启动的 server；但先启动的 server 不会自动知道后来出现的 client。

对于多数 CloudSuite client 主动连接 server 的场景，这个顺序够用。

### 每个 guest 里需要检查的东西

进入每个参与通信的 guest 后，先看：

```sh
ip addr show lo
ip route
cat /proc/cmdline
```

每个 guest 至少应该有自己的 pod IP：

```text
10.240.0.x/32 dev lo
```

每个 client guest 应该能路由整个 pod CIDR：

```text
10.240.0.0/24 dev lo
```

如果 client guest 里没有 server IP，可以手动补：

```sh
ip addr add <server_pod_ip>/32 dev lo 2>/dev/null || true
ip route replace 10.240.0.0/24 dev lo
```

例如：

```sh
ip addr add 10.240.0.2/32 dev lo 2>/dev/null || true
ip route replace 10.240.0.0/24 dev lo
```

### CloudSuite 配置里要改什么

CloudSuite 程序不用知道 mkring，但它需要连接正确的 server IP 和端口。

例如，原来 CloudSuite 通过环境变量指定 server：

```text
SERVER_IP=...
SERVER_PORT=11211
```

现在应该改成 mkcri 分配出来的 pod IP：

```text
SERVER_IP=10.240.0.2
SERVER_PORT=11211
```

这不是改 benchmark 源码，只是部署配置或运行参数变化。

### 必须对齐的三元组

每条连接都要对齐三件事：

```text
1. CloudSuite 连接的 server IP
2. CloudSuite 连接的 server port
3. server subkernel 的 peer kernel ID
```

如果 CloudSuite client 连接：

```text
10.240.0.2:11211
```

那么 client guest 里的 `mknetproxy` 必须有：

```sh
-forward 10.240.0.2:11211=<server_peer_kernel_id>
```

例如 server 的 peer kernel ID 是 `1`，则：

```sh
-forward 10.240.0.2:11211=1
```

IP、端口、peer kernel ID 任意一个错了，连接都会失败或者被转发到错误 subkernel。

## 3. 每个参与通信的 guest 如何启动 mknetproxy

每个参与跨 subkernel 通信的 guest 都要启动 `mknetproxy`。

有三种常见角色。

### 3.1 只接收连接的 server guest

如果某个 guest 只作为 server/slave，接收其他 subkernel 发来的连接，可以这样启动：

```sh
/bin/mknetproxy -syscall 470 -target-host 127.0.0.1
```

其中：

```text
-syscall 470
```

是 `sys_mkring_transport` 的 syscall number，按你的实际内核配置修改。

`-target-host` 表示当远端连接进来时，本地 mknetproxy 要把连接转给哪里。它最终会拨：

```text
<target-host>:<目标端口>
```

选择规则是：

```text
如果 CloudSuite server 监听 127.0.0.1:PORT，用 -target-host 127.0.0.1。
如果监听 0.0.0.0:PORT，通常也可以用 -target-host 127.0.0.1。
如果只监听自己的 pod IP，比如 10.240.0.2:PORT，用 -target-host 10.240.0.2。
```

可以在 server guest 里检查监听地址：

```sh
ss -lntp 2>/dev/null || netstat -lntp 2>/dev/null
```

### 3.2 主动连接其他 pod 的 client guest

如果某个 guest 作为 client/master，需要连接其他 subkernel 上的 server，就需要为每个目标 IP/端口添加一条 `-forward`：

```sh
/bin/mknetproxy \
  -syscall 470 \
  -forward <remote_pod_ip>:<remote_port>=<remote_peer_kernel_id>
```

例如 client 要连接：

```text
server IP: 10.240.0.2
server port: 11211
server peer kernel ID: 1
```

则 client guest 启动：

```sh
/bin/mknetproxy -syscall 470 -forward 10.240.0.2:11211=1
```

这表示：

```text
client guest 里的程序连接 10.240.0.2:11211
  -> 本地 mknetproxy 接住
  -> 通过 mkring channel 4 发给 peer kernel ID 1
  -> server guest 的 mknetproxy 收到
  -> 转给 server 本地服务
```

### 3.3 既接收又主动连接的 guest

如果某个 guest 同时作为 server 和 client，可以用一个 `mknetproxy` 进程同时带 `-target-host` 和多个 `-forward`：

```sh
/bin/mknetproxy \
  -syscall 470 \
  -target-host 127.0.0.1 \
  -forward 10.240.0.2:11211=1 \
  -forward 10.240.0.3:11211=2
```

### 3.4 一个 server、一个 client 的完整例子

假设：

```text
server pod IP:       10.240.0.2
server peer ID:      1
server port:         11211
client pod IP:       10.240.0.3
client peer ID:      2
mkring syscall nr:   470
```

server guest：

```sh
# 确认 CloudSuite server 已经监听 11211。
ss -lntp 2>/dev/null || netstat -lntp 2>/dev/null

# 启动接收侧 proxy。
/bin/mknetproxy -syscall 470 -target-host 127.0.0.1
```

client guest：

```sh
# 如果 client 里没有 server IP，就手动补。
ip addr add 10.240.0.2/32 dev lo 2>/dev/null || true
ip route replace 10.240.0.0/24 dev lo

# 启动转发侧 proxy。
/bin/mknetproxy -syscall 470 -forward 10.240.0.2:11211=1
```

CloudSuite client 配置：

```text
SERVER_IP=10.240.0.2
SERVER_PORT=11211
```

最终数据路径是：

```text
CloudSuite client
  -> TCP connect(10.240.0.2:11211)
  -> client guest lo
  -> client mknetproxy
  -> mkring channel 4
  -> server mknetproxy
  -> server local service at 127.0.0.1:11211
```

### 3.5 一个 client 连接多个 server 的例子

假设：

```text
server-0: 10.240.0.2, peer kernel ID = 1, port = 11211
server-1: 10.240.0.3, peer kernel ID = 2, port = 11211
client:   10.240.0.4, peer kernel ID = 3
```

每个 server guest 启动：

```sh
/bin/mknetproxy -syscall 470 -target-host 127.0.0.1
```

client guest 启动：

```sh
ip addr add 10.240.0.2/32 dev lo 2>/dev/null || true
ip addr add 10.240.0.3/32 dev lo 2>/dev/null || true
ip route replace 10.240.0.0/24 dev lo

/bin/mknetproxy \
  -syscall 470 \
  -forward 10.240.0.2:11211=1 \
  -forward 10.240.0.3:11211=2
```

### 3.6 一个 server 暴露多个端口的例子

如果 server `10.240.0.2` 同时暴露 `8080` 和 `11211`，client 侧要写两条 forward：

```sh
/bin/mknetproxy \
  -syscall 470 \
  -forward 10.240.0.2:8080=1 \
  -forward 10.240.0.2:11211=1
```

## 4. Subkernel 如何确定 IP 和 kernel ID 的对应关系

这里有两个标识，必须区分：

```text
pod IP：CloudSuite 使用的 IP，例如 10.240.0.2。
peer kernel ID：mkring 路由使用的数字 ID，例如 1。
```

这两个标识都由 host 侧 `mkcri` 管理。

### 4.1 peer kernel ID 从哪里来

`mkcri` 启动 subkernel 时会分配一个 `peer kernel ID`，并调用外部 start command。调用时会注入这些环境变量：

```text
MK_KERNEL_ID=<opaque-kernel-id>
MK_KERNEL_PEER_ID=<numeric-peer-kernel-id>
MK_KERNEL_BOOT_MODE=<cold_boot|snapshot>
```

你的 `start-subkernel` 脚本需要把 `MK_KERNEL_PEER_ID` 传给 guest kernel / mkring，一般是通过 kernel cmdline：

```text
mkring.kernel_id=<MK_KERNEL_PEER_ID>
```

进入 guest 后，可以查看：

```sh
cat /proc/cmdline
```

找到类似：

```text
mkring.kernel_id=1
```

这个数字就是 `mknetproxy -forward ...=<peer_kernel_id>` 里要填的目标 ID。

通常：

```text
host kernel: peer kernel ID = 0
subkernel 1: peer kernel ID = 1
subkernel 2: peer kernel ID = 2
subkernel 3: peer kernel ID = 3
```

但实际以 `mkcri` 分配和 `/proc/cmdline` 为准。

### 4.2 pod IP 从哪里来

`mkcri` 会从配置的 pod CIDR 里分配 IP。默认是：

```text
MK_POD_CIDR_BASE=10.240.0.0
MK_POD_CIDR_MASK=24
```

container 创建后，在 guest 里查看：

```sh
ip addr show lo
```

例如看到：

```text
inet 10.240.0.2/32 scope global lo
```

那么这个 guest 的 pod IP 就是：

```text
10.240.0.2
```

CloudSuite 里要使用这个 IP 作为 server/client 配置。

### 4.3 如何手动建立 IP 到 kernel ID 的映射表

对每个 guest 执行：

```sh
cat /proc/cmdline
ip addr show lo
```

然后整理成表：

```text
guest       pod IP        peer kernel ID    role       ports
server-0    10.240.0.2    1                 server     11211
client-0    10.240.0.3    2                 client     -
```

如果 client 要连接 server-0，那么 client 侧 `mknetproxy` 命令就是：

```sh
/bin/mknetproxy -syscall 470 -forward 10.240.0.2:11211=1
```

这里：

```text
10.240.0.2 是 server-0 的 pod IP。
11211 是 CloudSuite 服务端口。
1 是 server-0 的 peer kernel ID。
```

## 5. 调试 checklist

### 5.1 检查 ConfigureNetwork 是否生效

guest 内执行：

```sh
ip addr show lo
ip route
```

期望看到：

```text
own pod IP 出现在 lo 上，形式是 /32。
pod CIDR 路由指向 lo。
```

如果只看到 `127.0.0.1/8`，说明 `ConfigureNetwork` 没有执行或执行失败。

### 5.2 检查 mknetproxy 是否启动

```sh
ps | grep mknetproxy
```

建议启动时把日志重定向出来：

```sh
/bin/mknetproxy -syscall 470 -forward 10.240.0.2:11211=1 \
  >/tmp/mknetproxy.log 2>&1 &
cat /tmp/mknetproxy.log
```

server 侧也可以：

```sh
/bin/mknetproxy -syscall 470 -target-host 127.0.0.1 \
  >/tmp/mknetproxy.log 2>&1 &
cat /tmp/mknetproxy.log
```

### 5.3 检查 server 服务是否真的监听

server guest 里执行：

```sh
ss -lntp 2>/dev/null || netstat -lntp 2>/dev/null
```

如果 CloudSuite server 没有监听目标端口，mknetproxy 即使收到远端连接，也无法转给本地服务。

### 5.4 检查 peer kernel ID 是否写错

如果 client 侧 `mknetproxy` 日志里有 open/send 失败，先看 server guest：

```sh
cat /proc/cmdline
```

确认 `-forward ...=<peer_kernel_id>` 里的 ID 和 server 的 `mkring.kernel_id` 一致。

### 5.5 检查 CloudSuite 目标地址是否和 forward 规则一致

如果 CloudSuite 实际连接：

```text
10.240.0.2:8080
```

那么 client 侧必须有：

```sh
-forward 10.240.0.2:8080=<server_peer_kernel_id>
```

只有：

```sh
-forward 10.240.0.2:11211=<server_peer_kernel_id>
```

不会拦截 `8080` 的连接。

## 6. 当前限制

当前手动版有这些限制：

```text
1. mknetproxy 还不会被 mk-guest-agent 自动启动。
2. 后启动 pod 出现后，已有 pod 不会自动重新下发 endpoint table。
3. 端口需要手动写进 mknetproxy -forward 规则。
4. 暂时没有 DNS / Kubernetes Service 语义，建议使用显式 pod IP。
5. 当前主要面向 TCP 型 CloudSuite 通信。
```

因此，为了先跑通 CloudSuite，推荐流程是：

```text
1. 先启动 server/slave pods。
2. 记录每个 server 的 pod IP 和 peer kernel ID。
3. 再启动 client/master pods。
4. 如果 client 里没有 server IP，手动 ip addr add 到 lo。
5. 每个参与通信的 guest 手动启动 mknetproxy。
6. CloudSuite 配置里使用显式 server pod IP 和端口。
```
