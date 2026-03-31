//go:build !linux
// +build !linux

package streaming

func rawMkringTransport(_ uintptr, _ uint32, _ uintptr) error {
	return ErrStreamTransportNotImplemented
}
