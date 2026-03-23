package cri

import (
	"context"
	"testing"
	"time"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"mk-container/pkg/agent"
	"mk-container/pkg/kernel"
	mkrt "mk-container/pkg/runtime"
	"mk-container/pkg/util"
)

type countingKernelManager struct{}

func (m *countingKernelManager) StartKernel(_ context.Context, req kernel.StartRequest) (kernel.KernelInstance, error) {
	return kernel.KernelInstance{
		KernelID:     req.KernelID,
		PeerKernelID: req.PeerKernelID,
		Endpoint:     "mkring://1?kernel_id=" + req.KernelID,
	}, nil
}

func (m *countingKernelManager) StopKernel(_ context.Context, _ string) error {
	return nil
}

type countingClient struct {
	readLogCalls int
}

func (c *countingClient) WaitReady(_ context.Context) error {
	return nil
}

func (c *countingClient) CreateContainer(_ context.Context, _ agent.ContainerSpec) (string, string, error) {
	return "ctr-test", "", nil
}

func (c *countingClient) StartContainer(_ context.Context, _ string) error {
	return nil
}

func (c *countingClient) StopContainer(_ context.Context, _ string, _ time.Duration) (int32, error) {
	return 0, nil
}

func (c *countingClient) RemoveContainer(_ context.Context, _ string) error {
	return nil
}

func (c *countingClient) ContainerStatus(_ context.Context, _ string) (*agent.ContainerStatus, error) {
	return &agent.ContainerStatus{State: "EXITED", ExitCode: 0}, nil
}

func (c *countingClient) ReadLog(_ context.Context, _ string, offset uint64, _ int) (*agent.LogChunk, error) {
	c.readLogCalls++
	return &agent.LogChunk{NextOffset: offset, EOF: true}, nil
}

type countingFactory struct {
	client *countingClient
}

func (f *countingFactory) ForKernel(_, _ string) agent.Client {
	return f.client
}

func newCRITestServer(t *testing.T, client *countingClient) *Server {
	t.Helper()

	allocator, err := util.NewIPAllocator("10.240.0.0", 24)
	if err != nil {
		t.Fatalf("new allocator: %v", err)
	}
	kernelIDs, err := util.NewIntAllocator(1, 32)
	if err != nil {
		t.Fatalf("new kernel id allocator: %v", err)
	}

	engine := mkrt.NewEngine(&countingKernelManager{}, &countingFactory{client: client}, allocator, kernelIDs)
	return NewServer(engine, "mkcri", "test")
}

func TestContainerQueriesDoNotSyncLogs(t *testing.T) {
	client := &countingClient{}
	server := newCRITestServer(t, client)
	ctx := context.Background()

	runResp, err := server.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
		Config: &runtimeapi.PodSandboxConfig{
			Metadata: &runtimeapi.PodSandboxMetadata{
				Name:      "p",
				Namespace: "default",
				Uid:       "u1",
				Attempt:   1,
			},
		},
	})
	if err != nil {
		t.Fatalf("run pod sandbox: %v", err)
	}

	createResp, err := server.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{
		PodSandboxId: runResp.PodSandboxId,
		Config: &runtimeapi.ContainerConfig{
			Metadata: &runtimeapi.ContainerMetadata{
				Name:    "c",
				Attempt: 1,
			},
			Image: &runtimeapi.ImageSpec{Image: "busybox"},
		},
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}

	if _, err := server.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
		ContainerId: createResp.ContainerId,
	}); err != nil {
		t.Fatalf("container status: %v", err)
	}

	if _, err := server.ListContainers(ctx, &runtimeapi.ListContainersRequest{}); err != nil {
		t.Fatalf("list containers: %v", err)
	}

	if client.readLogCalls != 0 {
		t.Fatalf("expected container queries to avoid log sync, got %d ReadLog calls", client.readLogCalls)
	}
}
