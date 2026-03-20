package transport

import (
	"context"
	"time"

	"mkring-bridge/internal/protocol"
)

// Transport hides the host-side kernel/user bridge. A production implementation
// would translate Envelope values into mkring send/recv operations via a kernel
// adapter, miscdevice, or another syscall boundary.
type Transport interface {
	WaitReady(ctx context.Context, peerKernelID uint16, kernelID string, timeout time.Duration) error
	RoundTrip(ctx context.Context, peerKernelID uint16, req protocol.Envelope) (protocol.Envelope, error)
}
