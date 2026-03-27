package mkringcontrol

import (
	"context"
	"time"
)

// Transport hides the host-side kernel/user bridge. A production implementation
// would translate Envelope values into mkring send/recv operations via a kernel
// adapter, miscdevice, or another syscall boundary.
type Transport interface {
	WaitReady(ctx context.Context, peerKernelID uint16, kernelID string, timeout time.Duration) error
	ForcePeerReady(ctx context.Context, peerKernelID uint16, kernelID string) error
	RoundTrip(ctx context.Context, peerKernelID uint16, req Envelope) (Envelope, error)
}
