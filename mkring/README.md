# mkring (built-in)

`mkring` 是面向 multikernel 场景的 Linux 内核内置通信组件（不是设备，不通过 `insmod` 加载）。

核心特性：

- 使用 vring 风格（`desc/avail/used`）组织共享内存队列。
- 数据写入共享内存后，立即通过 IPI 通知目标 kernel。
- 任意 kernel `i -> j`（`i != j`）都可通信，方向独立（`N x N` 队列）。
- 初始化时通过 `request_mem_region("mkring-shm")` 在 `/proc/iomem` 注册共享内存区。

## 文件说明

- [mkring.c](./mkring.c)：核心 ring + 通信实现
- [mkring.h](./mkring.h)：对外接口（`mkring_init()`、`mkring_send()` 等）
- [mkring_container.h](./mkring_container.h)：container 控制面的消息头、payload 和 ioctl 定义
- [mkring_container_bridge.c](./mkring_container_bridge.c)：参考 `mkring_test.c` 风格实现的 container bridge 模块
- [init_mk.c](./init_mk.c)：kernel cmdline 参数解析 + init 阶段调用入口
- [Makefile](./Makefile)：内核树 `Kbuild` 片段（`obj-y`）

## 初始化方式（内置）

`mkring` 不再使用 `module_param/module_init`，而是通过：

1. `init_mk.c` 解析 kernel command line 参数（`__setup`）
2. 在 `subsys_initcall(init_mk)` 阶段调用 `mkring_init(&params)`

如果你想在其他系统初始化阶段调用，移除 `subsys_initcall` 并手动调用 `init_mk()` 即可。

## kernel command line 参数

`init_mk.c` 支持以下参数（前缀 `mkring.`）：

- `mkring.shm_phys=`（必填）
- `mkring.shm_size=`（必填）
- `mkring.kernel_id=`（必填）
- `mkring.kernels=`（默认 `2`）
- `mkring.desc_num=`（默认 `256`）
- `mkring.msg_size=`（默认 `1024`）
- `mkring.ipi_vector=`（默认 `0xF2`，发送 IPI 使用的向量号）
- `mkring.ipi_dests=`（可选，逗号分隔 APIC 物理 ID 映射，按 `kernel_id` 下标排列）
- `mkring.force_init=`（默认 `0`）

示例：

```text
mkring.shm_phys=0x90000000 mkring.shm_size=0x200000 mkring.kernel_id=1 mkring.kernels=4 mkring.desc_num=256 mkring.msg_size=1024 mkring.ipi_vector=0xF2 mkring.ipi_dests=0x10,0x20,0x30,0x40
```

## API

- `int mkring_init(const struct mkring_boot_params *params);`
- `void mkring_exit(void);`
- `int mkring_send(u16 dst_kid, const void *data, u32 len);`
- `int mkring_recv(u16 src_kid, void *buf, u32 buf_len, u32 *out_len, long timeout);`
- `int mkring_register_rx_cb(u16 src_kid, mkring_rx_cb_t cb, void *priv);`
- `int mkring_register_ipi_notify(mkring_ipi_notify_t notify, void *priv);`
- `int mkring_get_info(struct mkring_info *info);`
- `void mkring_handle_ipi_all(void);`
- `void mkring_ipi_interrupt(void);`（建议 IPI vector handler 调用）

## Container 控制面扩展

为了支持 `mkcontainer -> sub-kernel -> containerd` 的控制链路，当前目录新增了一层专用协议：

- `mkring_container.h`
  定义了 container 专用消息头：
  - `magic = MKRING_CONTAINER_MAGIC`
  - `channel = MKRING_CONTAINER_CHANNEL`
  - `kind = READY / REQUEST / RESPONSE`
  - `operation = CREATE / START / STOP / REMOVE`
- `mkring_container_bridge.c`
  提供一个 loadable module，复用了 `mkring_test.c` 的思路：
  - 通过 `mkring_register_rx_cb()` 为所有远端 peer 注册回调
  - 中断上下文只做消息校验和入队
  - worker 线程在进程上下文处理 `READY / REQUEST / RESPONSE`
  - 通过 `/dev/mkring_container_bridge` 暴露 host/guest 用户态接口

### 用户态接口

`mkring_container_bridge` 的 miscdevice 使用 [mkring_container.h](./mkring_container.h) 里的 UAPI：

- `MKRING_CONTAINER_IOC_WAIT_READY`
  host 侧等待某个 sub-kernel 发来 `READY`
- `MKRING_CONTAINER_IOC_CALL`
  host 侧发送一个 container `REQUEST` 并同步等待 `RESPONSE`
- `MKRING_CONTAINER_IOC_SET_READY`
  guest 侧在 `containerd` 启动成功后通知 host
- `read()`
  guest 侧阻塞读取 host 发来的 `REQUEST`
- `write()`
  guest 侧把处理后的 `RESPONSE` 写回内核模块，再由模块发回 host

### 典型加载方式

- host kernel
  - `insmod mkring_container_bridge.ko role=host device_name=mkring_container_bridge`
- sub-kernel
  - `insmod mkring_container_bridge.ko role=guest device_name=mkring_container_bridge`

然后按下面顺序工作：

1. sub-kernel 完成 `mkring_init`
2. guest 用户态拉起 `containerd`
3. guest 用户态对 `/dev/mkring_container_bridge` 发 `MKRING_CONTAINER_IOC_SET_READY`
4. host 用户态对同名 device 发 `MKRING_CONTAINER_IOC_WAIT_READY`
5. host 用户态发 `MKRING_CONTAINER_IOC_CALL`
6. guest 用户态 `read()` 到 `REQUEST`，转发到 `containerd.sock`
7. guest 用户态 `write()` 回 `RESPONSE`

## 关键数据结构详解

### 1) 启动参数：`struct mkring_boot_params`（`mkring.h`）

该结构由 `init_mk.c` 从 kernel cmdline 解析后传给 `mkring_init()`。

| 成员 | 作用 |
|---|---|
| `shm_phys` | 共享内存物理基址。所有 kernel 必须看到同一物理区域。 |
| `shm_size` | 共享内存总大小（字节）。用于边界校验和布局容量校验。 |
| `kernel_id` | 本 kernel 的逻辑编号。用于定位本地收发方向队列。 |
| `kernels` | 系统内 kernel 总数。决定队列规模 `kernels * kernels`。 |
| `desc_num` | 每个方向队列的 descriptor 数量。影响并发与吞吐。 |
| `msg_size` | 单条消息最大长度。也用于每个 desc 的 data slot 大小。 |
| `force_init` | 强制重置共享区（通常仅 `kernel_id=0` 节点使用）。 |
| `notify` | 可选 IPI 通知回调。非空时 `mkring_init()` 内部会自动注册。 |
| `notify_priv` | `notify` 的私有上下文指针。 |

### 2) 共享内存全局元数据

#### `struct mkring_shm_hdr`（`mkring.c`）

共享区头，位于共享内存起始位置，负责“跨 kernel 一致配置”和初始化状态协同。

| 成员 | 作用 |
|---|---|
| `magic` | 魔数，识别共享区是否为 mkring 布局。 |
| `version` | 布局版本号，防止不同实现混用。 |
| `hdr_len` | 共享头长度（对齐后）。用于定位队列区起点。 |
| `kernels` | 共享区采用的 kernel 数量配置。 |
| `desc_num` | 共享区采用的每队列 descriptor 数。 |
| `msg_size` | 共享区采用的单消息最大长度。 |
| `queue_size` | 单个方向队列占用字节数（含 vring + data 区）。 |
| `total_size` | mkring 实际需要的总字节数。用于容量检查。 |
| `init_state` | 初始化状态机：`UNINIT/INITING/READY`。 |
| `ready_bitmap` | 各 kernel 就绪位图，bit=`kernel_id`。 |
| `reserved[6]` | 预留扩展字段。 |

#### `struct mkring_layout`（`mkring.c`）

描述“单个方向队列”内部各子区域偏移，运行时通过它计算 ring/data 指针。

| 成员 | 作用 |
|---|---|
| `desc_off` | descriptor table 起始偏移。 |
| `avail_off` | avail header 起始偏移。 |
| `avail_ring_off` | avail ring 数组起始偏移。 |
| `avail_event_off` | avail event 偏移（保留兼容位）。 |
| `used_off` | used header 起始偏移。 |
| `used_ring_off` | used ring 数组起始偏移。 |
| `used_event_off` | used event 偏移（保留兼容位）。 |
| `data_off` | payload data 区起始偏移。 |
| `queue_size` | 单方向队列总字节数（页对齐）。 |

### 2.1) 共享内存区域布局与划分

`mkring` 的共享内存按“两层布局”组织：

1. 全局层：`共享头 + N x N 方向队列`
2. 队列层：`desc + avail + used + data`

#### 全局层布局

共享物理区间（由 `mkring.shm_phys` / `mkring.shm_size` 指定）：

```text
[ shm_phys                                                        shm_phys + shm_size )
  |<------------------------------- 可用共享内存 ------------------------------->|

  +----------------------+----------------------+------------------ ... -----------+
  | struct mkring_shm_hdr| queue(src=0,dst=0)  | queue(src=0,dst=1)               |
  | (hdr_len, 对齐后)    | (queue_size bytes)  | (queue_size bytes)               |
  +----------------------+----------------------+------------------ ... -----------+
                                                                     ... queue(N-1,N-1)
```

- `hdr_len = ALIGN(sizeof(struct mkring_shm_hdr), MKRING_ALIGN)`  
- 方向队列总数：`queue_count = kernels * kernels`  
- 每个方向队列大小：`queue_size = layout.queue_size`  
- `queue(src,dst)` 的偏移：
  - `qindex = src * kernels + dst`
  - `qoff = hdr_len + qindex * queue_size`

共享区最小需求（`mkring_prepare_shared()` 校验）：

```text
required = hdr_len + kernels * kernels * queue_size
```

#### `struct mkring_shm_hdr` 的头部布局

`mkring_shm_hdr` 位于共享区起始地址 `shm_phys + 0`，用于描述整片共享区的全局配置和状态机。

典型布局（按当前字段定义顺序）：

```text
offset  size  field
0x00    4     magic
0x04    2     version
0x06    2     hdr_len
0x08    2     kernels
0x0A    2     desc_num
0x0C    4     msg_size
0x10    4     queue_size
0x14    4     total_size
0x18    4     init_state
0x1C    4     ready_bitmap
0x20    24    reserved[6]
```

- `sizeof(struct mkring_shm_hdr)` 在当前实现下通常为 `56` 字节（`0x38`）。
- `hdr_len` 不是直接等于 `sizeof(...)`，而是：
  - `hdr_len = ALIGN(sizeof(struct mkring_shm_hdr), MKRING_ALIGN)`
- 当前 `MKRING_ALIGN = 64`，所以常见情况下 `hdr_len = 64`，即 header 后会有对齐填充，再开始第一个 queue。

#### 队列层布局（单个 queue 内部）

单个方向队列内部偏移由 `mkring_calc_layout(desc_num, msg_size, &layout)` 计算：

```text
queue_base + 0
  +-------------------------------+
  | desc table                    | ndesc * sizeof(struct mkring_desc)
  +-------------------------------+
  | padding to 2-byte align       |
  +-------------------------------+
  | avail hdr                     | sizeof(struct mkring_avail_hdr)
  +-------------------------------+
  | avail ring[]                  | ndesc * sizeof(__le16)
  +-------------------------------+
  | avail_event                   | sizeof(__le16)
  +-------------------------------+
  | padding to 4-byte align       |
  +-------------------------------+
  | used hdr                      | sizeof(struct mkring_used_hdr)
  +-------------------------------+
  | used ring[]                   | ndesc * sizeof(struct mkring_used_elem)
  +-------------------------------+
  | used_event                    | sizeof(__le16)
  +-------------------------------+
  | padding to 64-byte align      |
  +-------------------------------+
  | data slots                    | ndesc * msg_size
  +-------------------------------+
  | padding to PAGE_SIZE align    |
  +-------------------------------+
```

关键公式（对应 `mkring_calc_layout()`）：

```text
desc_bytes  = ndesc * sizeof(struct mkring_desc)
avail_bytes = sizeof(struct mkring_avail_hdr) + ndesc * sizeof(__le16) + sizeof(__le16)
used_bytes  = sizeof(struct mkring_used_hdr)  + ndesc * sizeof(struct mkring_used_elem) + sizeof(__le16)

desc_off    = 0
avail_off   = ALIGN(desc_off + desc_bytes, 2)
used_off    = ALIGN(avail_off + avail_bytes, 4)
data_off    = ALIGN(used_off + used_bytes, MKRING_ALIGN)
queue_size  = ALIGN(data_off + ndesc * msg_size, PAGE_SIZE)
```

#### 为什么是 `N x N`

- `queue(src,dst)` 表示“从 `src` 到 `dst`”的单向通道。
- 因为方向独立，`A->B` 与 `B->A` 使用不同队列，互不抢占 descriptor。
- 本地初始化时：
  - `txq[peer]` 绑定 `queue(local_id, peer)`
  - `rxq[peer]` 绑定 `queue(peer, local_id)`

### 3) vring 基础元素（共享内存内）

#### `struct mkring_desc`

| 成员 | 作用 |
|---|---|
| `addr` | 该 desc 对应 data slot 的物理地址。 |
| `len` | 当前消息长度（发送时写入，接收时读取）。 |
| `flags` | 预留 flags（当前实现未使用链式特性）。 |
| `next` | 预留 next desc（当前实现未使用）。 |

#### `struct mkring_avail_hdr`

| 成员 | 作用 |
|---|---|
| `flags` | avail ring 标志位（当前实现未启用事件抑制）。 |
| `idx` | 生产者索引；发送端每入队一条消息递增。 |

#### `struct mkring_used_elem`

| 成员 | 作用 |
|---|---|
| `id` | 已消费 descriptor 的 id。 |
| `len` | 已消费消息长度。 |

#### `struct mkring_used_hdr`

| 成员 | 作用 |
|---|---|
| `flags` | used ring 标志位（当前实现未启用事件抑制）。 |
| `idx` | 消费者索引；接收端每处理一条消息递增。 |

### 3.1) `avail ring` 与 `used ring` 的协同机制（详细）

可以把它理解为“一对提交/完成队列”：

- `avail ring`：发送端提交“可读的 desc id”
- `used ring`：接收端回执“已处理的 desc id”

#### 双方维护的进度指针

- 发送端生产 `avail->idx`，接收端用本地 `last_avail_idx` 追赶它
- 接收端生产 `used->idx`，发送端用本地 `last_used_idx` 追赶它
- ring 槽位都通过 `slot = idx % desc_num` 回绕

#### 一条消息的完整流转

1. 发送端先回收  
   调 `mkring_tx_reclaim_locked()` 读取 `used->idx/used->ring`，把已完成 id 从 `inflight bitmap` 清掉，恢复 `free_cnt`。
2. 发送端发布  
   分配一个空闲 `id`，写 `data[id]` 和 `desc[id].len`，然后按顺序写 `avail->ring[slot]=id`、`avail->idx++`。
3. 接收端消费  
   读取 `avail->idx`，循环处理 `last_avail_idx..avail_idx-1` 的每个 id，取 `desc/data` 并入本地接收队列。
4. 接收端回执  
   把同一个 `id` 写入 `used->ring`，再 `used->idx++`。
5. 发送端下次回收  
   下一次发送前再次扫 `used ring`，该 `id` 重新可分配。

#### 为什么必须有两个 ring

- 只有 `avail ring`：接收端能读消息，但发送端不知道何时可复用 desc。
- 加上 `used ring`：发送端获得“完成确认”，desc 才能安全循环复用。

#### 常见状态判断

- 接收侧“暂无新消息”：`last_avail_idx == avail->idx`
- 发送侧“队列满”：无空闲 desc（`free_cnt == 0` 或位图找不到空位），发送返回 `-EAGAIN`

### 4) 本地运行时队列视图：`struct mkring_queue`（`mkring.c`）

这是“某个远端 peer 对应的单方向队列”在本地内核中的运行时封装。

| 成员 | 作用 |
|---|---|
| `ctx` | 回指全局 `mkring_ctx`。 |
| `remote_id` | 对端 kernel id。 |
| `qindex` | 全局方向索引（`src * kernels + dst`）。 |
| `base` | 队列在共享内存中的虚拟地址基址。 |
| `phys` | 队列在共享内存中的物理地址基址。 |
| `inflight` | 发送中 desc 位图（仅 TX 队列使用）。 |
| `free_cnt` | 当前可用 desc 计数（仅 TX）。 |
| `last_used_idx` | 发送端已回收进度（对应 used.idx 游标）。 |
| `last_avail_idx` | 接收端已消费进度（对应 avail.idx 游标）。 |
| `tx_lock` | 保护发送入队和回收流程。 |
| `rx_lock` | 保护本地 `rx_msgs` 链表与回调注册字段。 |
| `proc_lock` | 保护 `mkring_process_rx_queue()`，避免并发重复消费。 |
| `rx_msgs` | 本地接收消息链表。 |
| `rx_wq` | 阻塞接收等待队列（`mkring_recv` 使用）。 |
| `rx_pending` | 本地待取消息计数。 |
| `cb` | 可选接收回调函数。 |
| `cb_priv` | 回调私有上下文。 |

### 5) 本地接收消息节点：`struct mkring_rx_msg`（`mkring.c`）

该结构不在共享内存里，只存在于本 kernel 内部，用于把共享区数据转成稳定本地副本。

| 成员 | 作用 |
|---|---|
| `node` | 链表节点，挂入 `mkring_queue.rx_msgs`。 |
| `len` | 消息长度。 |
| `data[]` | 可变长 payload 缓冲。 |

### 6) 全局上下文：`struct mkring_ctx`（`mkring.c`）

每个 kernel 进程空间只维护一个 `mkring_ctx` 实例，代表本地 mkring 实例状态。

| 成员 | 作用 |
|---|---|
| `shm_phys` / `shm_size` | 共享区物理地址与大小。 |
| `shm_base` | `memremap()` 后的虚拟地址。 |
| `hdr` / `hdr_len` | 共享区头指针与长度。 |
| `local_id` | 本地 kernel id。 |
| `kernels` / `desc_num` / `msg_size` | 关键运行参数。 |
| `layout` | 单方向队列布局缓存。 |
| `txq` | 发送队列数组，索引为目标 kernel id。 |
| `rxq` | 接收队列数组，索引为源 kernel id。 |
| `ipc_lock` | 保护通知回调注册/注销。 |
| `notify` / `notify_priv` | 发送完成后触发的 IPI 通知函数及私参。 |
| `force_init` | 是否启用强制初始化共享区。 |
| `ready` | 当前实例是否已完成初始化并可收发。 |

## 数据通信函数调用路径

下面给出从系统启动到一次完整收发的函数调用链路。

### 1) 启动初始化路径（built-in）

1. kernel 启动解析 cmdline：`__setup("mkring.xxx=", ...)`（`init_mk.c`）
2. `subsys_initcall(init_mk)` 触发 `init_mk()`
3. `init_mk()` 组装 `struct mkring_boot_params`，调用 `mkring_init(&params)`
4. `mkring_init()` 主要内部流程：
   - `mkring_calc_layout()`：计算每个方向队列的 vring 布局
   - `request_mem_region()`：注册共享内存资源到 `/proc/iomem`（名称 `mkring-shm`）
   - `memremap()`：映射共享内存
   - `mkring_prepare_shared()`：初始化/检查共享区头与所有方向队列
   - `mkring_queue_setup()`：建立本地 `txq[peer]` / `rxq[peer]` 视图

### 2) 发送路径（Kernel A -> Kernel B）

1. 上层调用：`mkring_send(dst_kid, data, len)`
2. `mkring_send()` 参数检查后先读取共享头 `ready_bitmap`，若目标 `dst_kid` 未就绪直接返回 `-ENOLINK`
3. 目标就绪时进入 `mkring_tx_enqueue(txq[dst], ...)`
4. `mkring_tx_enqueue()` 内部步骤：
   - `mkring_tx_reclaim_locked()` 回收已被远端写回 `used` 的 desc
   - 分配一个空闲 desc id（`inflight bitmap`）
   - `memcpy()` 把 payload 写入该 desc 对应共享 buffer
   - 更新 desc `len`
   - 写 `avail->ring[slot] = desc_id`
   - 写 `avail->idx++`（消息正式可见）
5. `mkring_send()` 调用已注册 IPI notify 回调：
   - `notify(src_kid, dst_kid, priv)`
   - 默认后端通过 APIC 向目标 kernel 发送 IPI

### 3) 到达通知与接收入队路径（Kernel B）

1. 目标 kernel 收到 IPI 后，在 IPI vector handler 中调用：
   - `mkring_ipi_interrupt()`（内部调用 `mkring_handle_ipi_all()`）
   - 或直接调用 `mkring_handle_ipi_all()`
2. 上述入口进入 `mkring_process_rx_queue(rxq[src])`
3. `mkring_process_rx_queue()` 内部步骤：
   - 读取 `avail->idx`，循环消费新 desc id
   - 从共享 buffer 取数据，调用 `mkring_rx_enqueue_local()` 放入本地 `rx_msgs` 链表
   - 同时写回 `used->ring` 和 `used->idx++`（告诉发送端该 desc 可回收）
4. `mkring_rx_enqueue_local()`：
   - 消息入本地队列
   - `wake_up_interruptible(&rx_wq)`
   - 若注册了回调，直接执行 `rx_cb(src, data, len, priv)`

### 4) 上层取数路径（两种模式）

模式 A：阻塞/超时接收  
1. 上层调用 `mkring_recv(src_kid, buf, buf_len, &out_len, timeout)`  
2. `wait_event_interruptible_timeout()` 等待 `rx_pending > 0`  
3. 从 `rx_msgs` 取首包，拷贝到用户提供缓冲区并返回

模式 B：回调接收  
1. 上层先调用 `mkring_register_rx_cb(src_kid, cb, priv)`  
2. 每次 `mkring_process_rx_queue()` 收到新包时，`mkring_rx_enqueue_local()` 触发 `cb()`  
3. 回调上下文通常是中断上下文，必须原子上下文安全

### 5) 发送端 desc 回收路径

1. 接收端每消费一条消息都会更新发送方向队列的 `used->idx`
2. 发送端下一次进入 `mkring_tx_enqueue()` 时先调用 `mkring_tx_reclaim_locked()`
3. `mkring_tx_reclaim_locked()` 扫描新 `used` 元素，清理 `inflight bitmap`，归还 desc

## IPI 通知模型

`mkring` 的默认通知后端是 IPI：

- 发送侧：`mkring_send()` 完成共享内存入队后，调用已注册的 `notify(src, dst, priv)`。
- 默认 notify（`init_mk.c`）通过 APIC 向目标 kernel 发送 IPI。
- 接收侧：IPI vector handler 调用 `mkring_ipi_interrupt()`（或 `mkring_handle_ipi_all()`）消费远端 avail ring。

### notify 注册逻辑说明

`mkring` 的 notify 注册支持两种方式：

1. 自动注册（推荐）
   - 在 `mkring_boot_params.notify` 传入回调
   - `mkring_init()` 内部调用 `mkring_register_ipi_notify()`
2. 手动注册
   - `mkring_init()` 后由平台侧显式调用 `mkring_register_ipi_notify(platform_notify, priv)`

`mkring_send()` 每次发送前会执行两类关键检查：

- 目标就绪检查：若 `ready_bitmap` 中目标 `dst_kid` 位未置位，返回 `-ENOLINK`
- notify 检查：`ctx->notify` 为空时返回 `-ENOTCONN`

检查通过后，才会入队并调用 `notify(src, dst, priv)`。

如需解绑，调用 `mkring_unregister_ipi_notify()`。

当前代码中对应位置：

- 注册实现：`mkring_register_ipi_notify()`（`mkring.c`）
- 自动注册调用点：`mkring_init()`（`mkring.c`，`params->notify` 非空时）
- 发送时检查与调用：`mkring_send()`（含 `ready_bitmap` 检查与 `notify` 检查）
- 注销实现：`mkring_unregister_ipi_notify()`（`mkring.c`）

建议时序：

1. `mkring_init()` 成功
2. 自动注册成功（或手动 `mkring_register_ipi_notify()` 成功）
3. 业务层开始调用 `mkring_send()`

### `init_mk.c` 中的 notify 来源（默认 IPI 后端）

`init_mk.c` 提供了 weak hook：

- `init_mk_get_notify(void **priv)`

默认实现逻辑如下：

1. 若未配置 `mkring.ipi_dests`，返回 `NULL`（即无默认 notify 后端）
2. 若配置了 `mkring.ipi_dests`，返回默认 IPI notify 回调
3. 默认 IPI notify 回调发送行为（x86）：
   - 取 `dst_kid` 对应 APIC 物理 ID（来自 `mkring.ipi_dests`）
   - 调用 `apic_icr_write(APIC_DM_FIXED | APIC_DEST_PHYSICAL | vector, apic_id)`
   - `vector` 来自 `mkring.ipi_vector`（默认 `0xF2`）
4. 目标 kernel 的 IPI vector handler 需要直接调用 `mkring_ipi_interrupt()`

你也可以在内核源码其他文件中提供同名强符号实现覆盖 `init_mk_get_notify()`，替换成平台专用 IPI 逻辑。

## 快速自测方案（双 kernel）

项目已提供可加载测试模块 `mkring_test.c`（`obj-m += mkring_test.o`）。

### 自测原理

- `insmod mkring_test.ko` 后，每个 kernel 启动一个发送线程，周期调用 `mkring_send(peer, ...)`。
- 同时在 `peer` 对应 `src_kid` 上注册 `mkring_register_rx_cb()` 回调。
- 回调收到合法测试包后累加 `rx_ok`，异常包累加 `rx_bad`。
- 周期性打印统计：`tx_ok/tx_fail/rx_ok/rx_bad`。

### 模块参数（insmod 传入）

- `peer=<id>`：对端 kernel_id（必填）
- `period_ms=<ms>`：发送周期，默认 `1000`
- `report_sec=<sec>`：统计打印周期，默认 `5`

### 启动参数示例（kernels=2）

kernel0：

```text
mkring.shm_phys=0x400000000 mkring.shm_size=0x1000000 mkring.kernels=2 mkring.desc_num=256 mkring.msg_size=1024 mkring.ipi_vector=0xF5 mkring.ipi_dests=0,1 mkring.force_init=1 mkring.kernel_id=0
```

kernel1：

```text
mkring.shm_phys=0x400000000 mkring.shm_size=0x1000000 mkring.kernels=2 mkring.desc_num=256 mkring.msg_size=1024 mkring.ipi_vector=0xF5 mkring.ipi_dests=0,1 mkring.force_init=0 mkring.kernel_id=1
```

### 加载测试模块

在 kernel0：

```bash
insmod mkring_test.ko peer=1 period_ms=200 report_sec=3
```

在 kernel1：

```bash
insmod mkring_test.ko peer=0 period_ms=200 report_sec=3
```

停止测试：

```bash
rmmod mkring_test
```

### 观测方法

在两个 kernel 分别观察 dmesg：

```bash
dmesg -w | grep -E "mkring|mkring-test"
```

### 通过判据

- 两侧都出现 `mkring-test: started ...`
- 周期日志里：
  - `tx_ok` 持续增长
  - `rx_ok` 持续增长
  - `tx_fail` 基本为 0（启动早期短暂 `ENOLINK` 可接受）
  - `rx_bad` 始终为 0

### 失败定位建议

- `tx_fail` 持续增长且 `rx_ok` 不增长：
  - 先确认 IPI vector 入口是否调用 `mkring_ipi_interrupt()`
  - 再确认 `mkring.ipi_vector` 与平台实际 vector 一致
  - 再确认 `mkring.ipi_dests` 是否为 `kernel_id -> APIC ID` 正确映射
  - 再确认 `insmod mkring_test.ko peer=<peer>` 的 `peer` 是否正确
- 出现 `-ENOTCONN`：
  - 说明 notify 未注册，检查 `init_mk_get_notify()` 与 `mkring.ipi_dests`
- 出现 `-ENOLINK`：
  - 说明目标 kernel 还没 ready，检查对端启动状态与参数一致性

## 注意

- 需要平台保证共享内存跨 kernel 可见性（cache coherence/映射属性一致）。
- 若 `request_mem_region("mkring-shm")` 失败（地址范围与现有资源冲突），`mkring_init()` 会返回 `-EBUSY`。
- `mkring_register_rx_cb()` 回调通常运行在中断上下文，回调代码必须原子上下文安全。
- 推荐冷启动全体 kernel；若需重置共享区，可由 `kernel_id=0` 且 `force_init=1` 的节点执行。
