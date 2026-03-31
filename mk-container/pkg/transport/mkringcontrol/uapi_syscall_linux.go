//go:build linux
// +build linux

package mkringcontrol

import "syscall"

const (
	MKRINGTransportOpSend uint32 = 1
	MKRINGTransportOpRecv uint32 = 2
)

func rawMkringTransport(trap uintptr, op uint32, arg uintptr) error {
	if trap == 0 {
		return ErrTransportUAPINotImplemented
	}

	_, _, errno := syscall.Syscall(trap, uintptr(op), arg, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
