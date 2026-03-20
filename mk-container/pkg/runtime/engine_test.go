package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"mk-container/pkg/agent"
	"mk-container/pkg/kernel"
	"mk-container/pkg/util"
)

type fakeKernelManager struct {
	stopCalls []string
}

func (f *fakeKernelManager) StartKernel(_ context.Context, req kernel.StartRequest) (kernel.KernelInstance, error) {
	return kernel.KernelInstance{
		KernelID:     req.KernelID,
		PeerKernelID: req.PeerKernelID,
		Endpoint:     "mkring://7?kernel_id=" + req.KernelID,
	}, nil
}

func (f *fakeKernelManager) StopKernel(_ context.Context, kernelID string) error {
	f.stopCalls = append(f.stopCalls, kernelID)
	return nil
}

type readyClient struct {
	waitErr    error
	waitCalls  int
	containers map[string]struct{}
}

func (c *readyClient) WaitReady(_ context.Context) error {
	c.waitCalls++
	return c.waitErr
}

func (c *readyClient) CreateContainer(_ context.Context, _ agent.ContainerSpec) (string, string, error) {
	if c.containers == nil {
		c.containers = map[string]struct{}{}
	}
	id := "ctr-test"
	c.containers[id] = struct{}{}
	return id, "", nil
}

func (c *readyClient) StartContainer(_ context.Context, _ string) error {
	return nil
}

func (c *readyClient) StopContainer(_ context.Context, _ string, _ time.Duration) (int32, error) {
	return 0, nil
}

func (c *readyClient) RemoveContainer(_ context.Context, _ string) error {
	return nil
}

func (c *readyClient) ContainerStatus(_ context.Context, _ string) (*agent.ContainerStatus, error) {
	return &agent.ContainerStatus{State: "RUNNING"}, nil
}

func (c *readyClient) ReadLog(_ context.Context, _ string, offset uint64, _ int) (*agent.LogChunk, error) {
	return &agent.LogChunk{NextOffset: offset, EOF: true}, nil
}

type readyFactory struct {
	client *readyClient
}

func (f *readyFactory) ForKernel(_, _ string) agent.Client {
	return f.client
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, _, _ := newTestEngineWithFactory(t, agent.NewMockFactory())
	return e
}

func newTestEngineWithFactory(t *testing.T, factory agent.Factory) (*Engine, *fakeKernelManager, *util.IntAllocator) {
	t.Helper()
	allocator, err := util.NewIPAllocator("10.240.0.0", 24)
	if err != nil {
		t.Fatalf("new allocator: %v", err)
	}
	kernelIDs, err := util.NewIntAllocator(1, 32)
	if err != nil {
		t.Fatalf("new kernel id allocator: %v", err)
	}
	km := &fakeKernelManager{}
	return NewEngine(km, factory, allocator, kernelIDs), km, kernelIDs
}

func TestEngineLifecycle(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	pod, err := e.RunPod(ctx, PodSpec{Name: "p", Namespace: "default", UID: "u1"})
	if err != nil {
		t.Fatalf("run pod: %v", err)
	}

	ctr, err := e.CreateContainer(ctx, ContainerSpec{PodID: pod.ID, Name: "c", Image: "busybox"})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}

	if err := e.StartContainer(ctx, ctr.ID); err != nil {
		t.Fatalf("start container: %v", err)
	}

	if err := e.StopContainer(ctx, ctr.ID, 2*time.Second); err != nil {
		t.Fatalf("stop container: %v", err)
	}

	if err := e.RemoveContainer(ctx, ctr.ID); err != nil {
		t.Fatalf("remove container: %v", err)
	}

	if err := e.StopPod(ctx, pod.ID); err != nil {
		t.Fatalf("stop pod: %v", err)
	}

	if err := e.RemovePod(ctx, pod.ID); err != nil {
		t.Fatalf("remove pod: %v", err)
	}
}

func TestOneKernelOneContainer(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	pod, err := e.RunPod(ctx, PodSpec{Name: "p", Namespace: "default", UID: "u1"})
	if err != nil {
		t.Fatalf("run pod: %v", err)
	}

	if _, err := e.CreateContainer(ctx, ContainerSpec{PodID: pod.ID, Name: "c1", Image: "img"}); err != nil {
		t.Fatalf("create first container: %v", err)
	}

	if _, err := e.CreateContainer(ctx, ContainerSpec{PodID: pod.ID, Name: "c2", Image: "img"}); err == nil {
		t.Fatalf("expected second container creation to fail")
	}
}

func TestRunPodWaitsForGuestReady(t *testing.T) {
	client := &readyClient{}
	e, _, _ := newTestEngineWithFactory(t, &readyFactory{client: client})

	if _, err := e.RunPod(context.Background(), PodSpec{Name: "p", Namespace: "default", UID: "u1"}); err != nil {
		t.Fatalf("run pod: %v", err)
	}

	if client.waitCalls != 1 {
		t.Fatalf("expected wait ready to be called once, got %d", client.waitCalls)
	}
}

func TestRunPodReadyFailureRollsBackKernel(t *testing.T) {
	client := &readyClient{waitErr: errors.New("not ready")}
	e, km, kernelIDs := newTestEngineWithFactory(t, &readyFactory{client: client})

	if _, err := e.RunPod(context.Background(), PodSpec{Name: "p", Namespace: "default", UID: "u1"}); err == nil {
		t.Fatalf("expected run pod to fail")
	}

	if client.waitCalls != 1 {
		t.Fatalf("expected wait ready to be called once, got %d", client.waitCalls)
	}
	if len(km.stopCalls) != 1 {
		t.Fatalf("expected stop kernel to be called once, got %d", len(km.stopCalls))
	}

	peerID, err := kernelIDs.Allocate()
	if err != nil {
		t.Fatalf("reallocate peer kernel id: %v", err)
	}
	if peerID != 1 {
		t.Fatalf("expected released peer kernel id to be reusable, got %d", peerID)
	}
}
