# Phase 2 Transport UAPI Diagram

This note captures the Phase 2 direction after narrowing the kernel boundary:

- keep `MKRING_CHANNEL_SYSTEM` reserved and unused for now
- remove `/dev/mkring_container_bridge`
- keep `/dev/mkring_stream_bridge` unchanged
- let the kernel provide only generic transport semantics
- keep message validity, ready handling, and request/response matching in userspace

## 1. Target Relationship

```mermaid
flowchart LR

    subgraph HOST["Host Userspace"]
        CRI["mkcri / host monitor"]
        AGENTCLIENT["agent.MkringFactory\nagent.mkringClient"]
        SERVICE["mkringcontrol.Service"]
        UAPIT["mkringcontrol.UAPITransport"]
        HOSTSTATE["userspace control state\nready map / pending map"]
    end

    subgraph HK["Host Kernel"]
        HUAPI["sys_mkring_transport\nSEND / RECV"]
        CORE["mkring transport core"]
    end

    subgraph GK["Guest Kernel"]
        GUAPI["sys_mkring_transport\nSEND / RECV"]
    end

    subgraph GUEST["Guest Userspace"]
        GTRAN["transport_mkring.c"]
        GAGENT["mk-guest-agent"]
        RUNTIME["containerd / ctr / runc"]
    end

    CRI --> AGENTCLIENT
    AGENTCLIENT --> SERVICE
    SERVICE --> UAPIT
    UAPIT --> HOSTSTATE
    UAPIT --> HUAPI
    HUAPI --> CORE
    CORE --> GUAPI
    GUAPI --> GTRAN
    GTRAN --> GAGENT
    GAGENT --> RUNTIME
```

## 2. Container Control Flow

```mermaid
sequenceDiagram
    participant H as "mkcri / host monitor"
    participant T as "UAPITransport"
    participant HU as "sys_mkring_transport(host)"
    participant MK as "mkring"
    participant GU as "sys_mkring_transport(guest)"
    participant GT as "transport_mkring.c"
    participant GA as "mk-guest-agent"

    H->>T: CreateContainer / StartContainer / StopContainer / Status / ReadLog / ExecTTY*
    T->>T: encode channel-1 REQUEST message
    T->>HU: SEND(peer, channel=1, message)
    HU->>MK: forward opaque bytes
    MK->>GU: deliver opaque bytes
    GU->>GT: RECV(channel=1)
    GT->>GT: decode REQUEST in userspace
    GT->>GA: mkga_envelope request
    GA-->>GT: mkga_envelope response
    GT->>GT: encode RESPONSE in userspace
    GT->>GU: SEND(peer, channel=1, message)
    GU->>MK: forward opaque bytes
    MK->>HU: deliver opaque bytes
    HU->>T: RECV(channel=1)
    T->>T: match response by request_id in userspace
    T-->>H: decoded response
```

## 3. Ready Flow

```mermaid
sequenceDiagram
    participant H as "mkcri / host monitor"
    participant T as "UAPITransport"
    participant MK as "mkring"
    participant GT as "transport_mkring.c"
    participant GA as "mk-guest-agent"

    GA->>GT: transport init
    GT->>GT: encode READY message in userspace
    GT->>MK: SEND(channel=1, READY)
    MK->>T: RECV(channel=1, READY)
    T->>T: mark peer ready in userspace
    H->>T: WaitReady(peer)
    T-->>H: ready
```

Snapshot still keeps a userspace-only shortcut:

- host may mark a peer ready locally before a READY message arrives
- the kernel is not responsible for snapshot-specific ready state

## 4. Kernel Boundary

The kernel-side transport UAPI should do only this:

- validate the basic `mkring` transport header
- validate the destination peer and supported channel
- queue opaque messages by channel
- block in `RECV` until a message is available or timeout expires
- forward opaque bytes through `mkring_send()`

The kernel should not do this:

- interpret container operations
- validate container payload shapes
- maintain per-peer ready state
- match request/response ids
- know what `CREATE`, `STOP`, or `EXEC_TTY_*` mean

## 5. Phase 2 Runtime Path

After cutover, the control path becomes:

```text
mkcri
  -> mkringcontrol.UAPITransport
  -> sys_mkring_transport (host SEND/RECV)
  -> mkring
  -> sys_mkring_transport (guest SEND/RECV)
  -> transport_mkring.c
  -> mk-guest-agent
```

The stream path stays unchanged in Phase 2:

```text
mkcri streaming
  -> /dev/mkring_stream_bridge
  -> mkring channel 3
  -> guest PTY/session path
```

## 6. File Mapping

Kernel-side:

- [mkring_transport_uapi.h](/Users/yezhucan/Desktop/mk%20container/mkring/mkring_transport_uapi.h)
  - generic direct-entry transport UAPI
- [mkring_transport_syscall.c](/Users/yezhucan/Desktop/mk%20container/mkring/mkring_transport_syscall.c)
  - staging implementation of `sys_mkring_transport`
  - channel queueing and send/recv dispatch only

Host-side:

- [uapi.go](/Users/yezhucan/Desktop/mk%20container/mk-container/pkg/transport/mkringcontrol/uapi.go)
  - userspace ready tracking and pending-response matching
- [uapi_syscall.go](/Users/yezhucan/Desktop/mk%20container/mk-container/pkg/transport/mkringcontrol/uapi_syscall.go)
  - syscall-backed `SEND` / `RECV`

Guest-side:

- [transport_mkring.c](/Users/yezhucan/Desktop/mk%20container/mk-guest-agent/src/transport_mkring.c)
  - userspace READY publication
  - userspace REQUEST decode / RESPONSE encode

## 7. What Still Belongs To Phase 3

Phase 2 does not touch:

- `/dev/mkring_stream_bridge`
- stream session/data plane behavior
- SPDY/remotecommand host front-end
