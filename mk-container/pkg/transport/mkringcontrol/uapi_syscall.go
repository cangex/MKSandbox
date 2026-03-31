package mkringcontrol

import (
	"context"
	"fmt"
	"runtime"
	"unsafe"
)

const (
	transportSendSize = 1032
	transportRecvSize = 1036
)

// SyscallHostTransportUAPI is the host-side direct-entry backend for the Phase 2
// transport path. The trap number must match the running kernel's
// sys_mkring_transport syscall number.
type SyscallHostTransportUAPI struct {
	trap uintptr
}

func NewSyscallHostTransportUAPI(trap uintptr) *SyscallHostTransportUAPI {
	return &SyscallHostTransportUAPI{trap: trap}
}

func (d *SyscallHostTransportUAPI) Send(ctx context.Context, req TransportSend) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	buf, err := encodeStruct(req, transportSendSize)
	if err != nil {
		return err
	}
	if err := invokeMkringTransport(d.trap, MKRINGTransportOpSend, buf); err != nil {
		return fmt.Errorf("send via direct entry: %w", err)
	}
	return nil
}

func (d *SyscallHostTransportUAPI) Recv(ctx context.Context, req TransportRecv) (TransportRecv, error) {
	if err := ctx.Err(); err != nil {
		return TransportRecv{}, err
	}

	buf, err := encodeStruct(req, transportRecvSize)
	if err != nil {
		return TransportRecv{}, err
	}
	if err := invokeMkringTransport(d.trap, MKRINGTransportOpRecv, buf); err != nil {
		return TransportRecv{}, fmt.Errorf("recv via direct entry: %w", err)
	}

	var result TransportRecv
	if err := decodeStruct(buf, &result); err != nil {
		return TransportRecv{}, err
	}
	return result, nil
}

func invokeMkringTransport(trap uintptr, op uint32, arg []byte) error {
	if len(arg) == 0 {
		return fmt.Errorf("empty direct-entry buffer")
	}

	err := rawMkringTransport(trap, op, uintptr(unsafe.Pointer(&arg[0])))
	runtime.KeepAlive(arg)
	return err
}
