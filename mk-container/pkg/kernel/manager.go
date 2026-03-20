package kernel

import "context"

type StartRequest struct {
	KernelID     string
	PeerKernelID uint16
	PodID        string
	Namespace    string
	Name         string
}

type KernelInstance struct {
	KernelID     string
	PeerKernelID uint16
	Endpoint     string
}

// Manager controls sub-kernel lifecycle.
type Manager interface {
	StartKernel(ctx context.Context, req StartRequest) (KernelInstance, error)
	StopKernel(ctx context.Context, kernelID string) error
}
