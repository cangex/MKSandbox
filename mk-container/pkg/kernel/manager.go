package kernel

import "context"

type BootMode string

const (
	BootModeColdBoot BootMode = "cold_boot"
	BootModeSnapshot BootMode = "snapshot"
)

type StartRequest struct {
	KernelID     string
	PeerKernelID uint16
	PodID        string
	Namespace    string
	Name         string
	BootMode     BootMode
}

type KernelInstance struct {
	KernelID      string
	PeerKernelID  uint16
	Endpoint      string
	SkipWaitReady bool
}

// Manager controls sub-kernel lifecycle.
type Manager interface {
	StartKernel(ctx context.Context, req StartRequest) (KernelInstance, error)
	StopKernel(ctx context.Context, kernelID string) error
}
