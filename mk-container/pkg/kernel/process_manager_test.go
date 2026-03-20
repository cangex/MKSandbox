package kernel

import (
	"context"
	"testing"
)

func TestProcessManagerStartKernelMkringEndpoint(t *testing.T) {
	m := NewProcessManager("", "", "unix:///ignored/%s.sock", "mkring")

	instance, err := m.StartKernel(context.Background(), StartRequest{
		KernelID:     "kernel-a",
		PeerKernelID: 7,
	})
	if err != nil {
		t.Fatalf("start kernel: %v", err)
	}

	if instance.Endpoint != "mkring://7?kernel_id=kernel-a" {
		t.Fatalf("unexpected endpoint: %s", instance.Endpoint)
	}
	if instance.PeerKernelID != 7 {
		t.Fatalf("unexpected peer kernel id: %d", instance.PeerKernelID)
	}
}
