//go:build !linux
// +build !linux

package mkringcontrol

const (
	MKRINGTransportOpSend uint32 = 1
	MKRINGTransportOpRecv uint32 = 2
)

func rawMkringTransport(trap uintptr, op uint32, arg uintptr) error {
	_ = trap
	_ = op
	_ = arg
	return ErrTransportUAPINotImplemented
}
