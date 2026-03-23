package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"mk-container/pkg/agent"
	"mk-container/pkg/kernel"
	"mk-container/pkg/model"
	"mk-container/pkg/util"
)

type fakeKernelManager struct {
	stopCalls []string
	stopStart chan struct{}
	stopBlock chan struct{}
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
	if f.stopStart != nil {
		select {
		case f.stopStart <- struct{}{}:
		default:
		}
	}
	if f.stopBlock != nil {
		<-f.stopBlock
	}
	return nil
}

type readyClient struct {
	waitErr    error
	waitCalls  int
	containers map[string]struct{}
	status     *agent.ContainerStatus
	logChunk   *agent.LogChunk
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
	if c.status != nil {
		cp := *c.status
		return &cp, nil
	}
	return &agent.ContainerStatus{State: "RUNNING"}, nil
}

func (c *readyClient) ReadLog(_ context.Context, _ string, offset uint64, _ int) (*agent.LogChunk, error) {
	if c.logChunk != nil {
		cp := *c.logChunk
		return &cp, nil
	}
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

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("condition not met within %s", timeout)
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

func TestRunPodFailureDoesNotConsumePodIP(t *testing.T) {
	client := &readyClient{waitErr: errors.New("not ready")}
	e, _, _ := newTestEngineWithFactory(t, &readyFactory{client: client})

	if _, err := e.RunPod(context.Background(), PodSpec{Name: "p1", Namespace: "default", UID: "u1"}); err == nil {
		t.Fatalf("expected first run pod to fail")
	}

	client.waitErr = nil
	pod, err := e.RunPod(context.Background(), PodSpec{Name: "p2", Namespace: "default", UID: "u2"})
	if err != nil {
		t.Fatalf("run pod after failure: %v", err)
	}
	if pod.IP != "10.240.0.2" {
		t.Fatalf("expected first allocatable pod ip 10.240.0.2 after rollback, got %s", pod.IP)
	}
}

func TestStopPodReleasesPodIP(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	firstPod, err := e.RunPod(ctx, PodSpec{Name: "p1", Namespace: "default", UID: "u1"})
	if err != nil {
		t.Fatalf("run first pod: %v", err)
	}
	if firstPod.IP != "10.240.0.2" {
		t.Fatalf("expected first pod ip 10.240.0.2, got %s", firstPod.IP)
	}

	if err := e.StopPod(ctx, firstPod.ID); err != nil {
		t.Fatalf("stop first pod: %v", err)
	}

	stoppedPod, ok := e.GetPod(firstPod.ID)
	if !ok {
		t.Fatalf("expected stopped pod to remain tracked")
	}
	if stoppedPod.IP != "" {
		t.Fatalf("expected stopped pod ip to be cleared, got %s", stoppedPod.IP)
	}

	secondPod, err := e.RunPod(ctx, PodSpec{Name: "p2", Namespace: "default", UID: "u2"})
	if err != nil {
		t.Fatalf("run second pod: %v", err)
	}
	if secondPod.IP != "10.240.0.2" {
		t.Fatalf("expected released pod ip 10.240.0.2 to be reused, got %s", secondPod.IP)
	}
}

func TestRemovePodReleasesPodIP(t *testing.T) {
	e := newTestEngine(t)
	ctx := context.Background()

	firstPod, err := e.RunPod(ctx, PodSpec{Name: "p1", Namespace: "default", UID: "u1"})
	if err != nil {
		t.Fatalf("run first pod: %v", err)
	}
	if firstPod.IP != "10.240.0.2" {
		t.Fatalf("expected first pod ip 10.240.0.2, got %s", firstPod.IP)
	}

	if err := e.RemovePod(ctx, firstPod.ID); err != nil {
		t.Fatalf("remove first pod: %v", err)
	}

	if _, ok := e.GetPod(firstPod.ID); ok {
		t.Fatalf("expected removed pod to disappear from engine state")
	}

	secondPod, err := e.RunPod(ctx, PodSpec{Name: "p2", Namespace: "default", UID: "u2"})
	if err != nil {
		t.Fatalf("run second pod: %v", err)
	}
	if secondPod.IP != "10.240.0.2" {
		t.Fatalf("expected removed pod ip 10.240.0.2 to be reused, got %s", secondPod.IP)
	}
}

func TestMonitorAutoCleanupStopsPodAfterExitAndLogEOF(t *testing.T) {
	client := &readyClient{
		status: &agent.ContainerStatus{
			State:      "EXITED",
			ExitCode:   0,
			FinishedAt: time.Now().UTC(),
		},
		logChunk: &agent.LogChunk{EOF: true},
	}
	e, km, _ := newTestEngineWithFactory(t, &readyFactory{client: client})
	ctx := context.Background()

	firstPod, err := e.RunPod(ctx, PodSpec{Name: "p1", Namespace: "default", UID: "u1"})
	if err != nil {
		t.Fatalf("run first pod: %v", err)
	}

	ctr, err := e.CreateContainer(ctx, ContainerSpec{
		PodID:   firstPod.ID,
		Name:    "c1",
		Image:   "busybox",
		LogPath: filepath.Join(t.TempDir(), "ctr.log"),
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}

	if err := e.StartContainer(ctx, ctr.ID); err != nil {
		t.Fatalf("start container: %v", err)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		pod, ok := e.GetPod(firstPod.ID)
		return ok && pod.State == model.PodStateNotReady && pod.IP == ""
	})

	if len(km.stopCalls) != 1 {
		t.Fatalf("expected auto cleanup to stop kernel once, got %d", len(km.stopCalls))
	}

	updatedCtr, ok := e.GetContainer(ctr.ID)
	if !ok {
		t.Fatalf("expected container record to remain after auto cleanup")
	}
	if updatedCtr.State != model.ContainerStateExited {
		t.Fatalf("expected container to remain EXITED, got %s", updatedCtr.State)
	}

	secondPod, err := e.RunPod(ctx, PodSpec{Name: "p2", Namespace: "default", UID: "u2"})
	if err != nil {
		t.Fatalf("run second pod: %v", err)
	}
	if secondPod.IP != firstPod.IP {
		t.Fatalf("expected auto-cleaned pod ip %s to be reused, got %s", firstPod.IP, secondPod.IP)
	}
}

func TestRemovePodWaitsForAutoCleanup(t *testing.T) {
	allocator, err := util.NewIPAllocator("10.240.0.0", 24)
	if err != nil {
		t.Fatalf("new allocator: %v", err)
	}
	kernelIDs, err := util.NewIntAllocator(1, 32)
	if err != nil {
		t.Fatalf("new kernel id allocator: %v", err)
	}
	km := &fakeKernelManager{
		stopStart: make(chan struct{}, 1),
		stopBlock: make(chan struct{}),
	}
	client := &readyClient{
		status: &agent.ContainerStatus{
			State:      "EXITED",
			ExitCode:   0,
			FinishedAt: time.Now().UTC(),
		},
		logChunk: &agent.LogChunk{EOF: true},
	}
	e := NewEngine(km, &readyFactory{client: client}, allocator, kernelIDs)
	ctx := context.Background()

	pod, err := e.RunPod(ctx, PodSpec{Name: "p1", Namespace: "default", UID: "u1"})
	if err != nil {
		t.Fatalf("run pod: %v", err)
	}
	ctr, err := e.CreateContainer(ctx, ContainerSpec{
		PodID:   pod.ID,
		Name:    "c1",
		Image:   "busybox",
		LogPath: filepath.Join(t.TempDir(), "ctr.log"),
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if err := e.StartContainer(ctx, ctr.ID); err != nil {
		t.Fatalf("start container: %v", err)
	}

	select {
	case <-km.stopStart:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for auto cleanup to enter StopKernel")
	}

	removeDone := make(chan error, 1)
	go func() {
		removeDone <- e.RemovePod(ctx, pod.ID)
	}()

	select {
	case err := <-removeDone:
		t.Fatalf("expected RemovePod to wait for in-flight cleanup, returned early with %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(km.stopBlock)

	select {
	case err := <-removeDone:
		if err != nil {
			t.Fatalf("remove pod after auto cleanup: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for RemovePod to finish")
	}

	if len(km.stopCalls) != 1 {
		t.Fatalf("expected only one StopKernel call, got %d", len(km.stopCalls))
	}
	if _, ok := e.GetPod(pod.ID); ok {
		t.Fatalf("expected pod to be removed after RemovePod wins cleanup tail")
	}
}
