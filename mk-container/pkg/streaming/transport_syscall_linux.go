//go:build linux
// +build linux

package streaming

import "syscall"

func rawMkringTransport(trap uintptr, op uint32, arg uintptr) error {
	if trap == 0 {
		return ErrStreamTransportNotImplemented
	}

	_, _, errno := syscall.Syscall(trap, uintptr(op), arg, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
