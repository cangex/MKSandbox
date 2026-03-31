package streaming

import (
	"context"
	"fmt"
	"unsafe"
)

type SyscallStreamTransportUAPI struct {
	trap uintptr
}

func NewSyscallStreamBridge(trap uintptr) *TransportBridge {
	return NewTransportBridge(&SyscallStreamTransportUAPI{trap: trap})
}

func (d *SyscallStreamTransportUAPI) Send(ctx context.Context, req transportSend) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rawMkringTransport(d.trap, streamTransportSendOp, uintptr(unsafe.Pointer(&req))); err != nil {
		return fmt.Errorf("send via direct entry: %w", err)
	}
	return nil
}

func (d *SyscallStreamTransportUAPI) Recv(ctx context.Context, req transportRecv) (transportRecv, error) {
	if err := ctx.Err(); err != nil {
		return transportRecv{}, err
	}
	if err := rawMkringTransport(d.trap, streamTransportRecvOp, uintptr(unsafe.Pointer(&req))); err != nil {
		return transportRecv{}, fmt.Errorf("recv via direct entry: %w", err)
	}
	return req, nil
}
