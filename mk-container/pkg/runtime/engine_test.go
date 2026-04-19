package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mk-container/pkg/agent"
	"mk-container/pkg/kernel"
	"mk-container/pkg/model"
	"mk-container/pkg/util"
)

type fakeKernelManager struct {
	stopCalls        []string
	stopStart        chan struct{}
	stopBlock        chan struct{}
	lastStartRequest kernel.StartRequest
}

func (f *fakeKernelManager) StartKernel(_ context.Context, req kernel.StartRequest) (kernel.KernelInstance, error) {
	f.lastStartRequest = req
	return kernel.KernelInstance{
		KernelID:      req.KernelID,
		PeerKernelID:  req.PeerKernelID,
		Endpoint:      "mkring://7?kernel_id=" + req.KernelID,
		SkipWaitReady: req.BootMode == kernel.BootModeSnapshot,
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
	waitErr         error
	waitCalls       int
	forceReadyCalls int
	containers      map[string]struct{}
	status          *agent.ContainerStatus
	logChunk        *agent.LogChunk
	stopErr         error
	stopCode        int32
	stopStart       chan struct{}
	stopBlock       chan struct{}
	stopTimeout     time.Duration
	mu              sync.Mutex
	stopped         bool
}

func (c *readyClient) WaitReady(_ context.Context) error {
	c.waitCalls++
	return c.waitErr
}

func (c *readyClient) ForcePeerReady(_ context.Context) error {
	c.forceReadyCalls++
	return nil
}

func (c *readyClient) ConfigureNetwork(_ context.Context, _ agent.NetworkSpec) error {
	return nil
}

func (c *readyClient) ConfigureContainerEnv(_ context.Context, _ string, _ string, _ []agent.EnvVar) error {
	return nil
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

func (c *readyClient) StopContainer(_ context.Context, _ string, timeout time.Duration) (int32, error) {
	if c.stopStart != nil {
		select {
		case c.stopStart <- struct{}{}:
		default:
		}
	}
	c.mu.Lock()
	c.stopTimeout = timeout
	c.stopped = true
	c.mu.Unlock()
	if c.stopBlock != nil {
		<-c.stopBlock
	}
	if c.stopErr != nil {
		return 0, c.stopErr
	}
	return c.stopCode, nil
}

func (c *readyClient) RemoveContainer(_ context.Context, _ string) error {
	return nil
}

func (c *readyClient) ContainerStatus(_ context.Context, _ string) (*agent.ContainerStatus, error) {
	c.mu.Lock()
	stopped := c.stopped
	c.mu.Unlock()
	if stopped {
		return &agent.ContainerStatus{
			State:      "EXITED",
			ExitCode:   c.stopCode,
			FinishedAt: time.Now().UTC(),
		}, nil
	}
	if c.status != nil {
		cp := *c.status
		return &cp, nil
	}
	return &agent.ContainerStatus{State: "RUNNING"}, nil
}

func (c *readyClient) ReadLog(_ context.Context, _ string, offset uint64, _ int) (*agent.LogChunk, error) {
	c.mu.Lock()
	stopped := c.stopped
	c.mu.Unlock()
	if stopped {
		return &agent.LogChunk{NextOffset: offset, EOF: true}, nil
	}
	if c.logChunk != nil {
		cp := *c.logChunk
		return &cp, nil
	}
	return &agent.LogChunk{NextOffset: offset, EOF: true}, nil
}

func (c *readyClient) ExecTTYPrepare(_ context.Context, _ agent.ExecTTYRequest) (*agent.ExecTTYPrepareResult, error) {
	return nil, agent.ErrNotImplemented
}

func (c *readyClient) ExecTTYStart(_ context.Context, _ agent.ExecTTYStartRequest) error {
	return agent.ErrNotImplemented
}

func (c *readyClient) ExecTTYResize(_ context.Context, _ agent.ExecTTYResizeRequest) error {
	return agent.ErrNotImplemented
}

func (c *readyClient) ExecTTYClose(_ context.Context, _ agent.ExecTTYCloseRequest) error {
	return agent.ErrNotImplemented
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

func TestCreateContainerForSnapshotPodForcesPeerReady(t *testing.T) {
	client := &readyClient{}
	e, _, _ := newTestEngineWithFactory(t, &readyFactory{client: client})
	ctx := context.Background()

	pod, err := e.RunPod(ctx, PodSpec{
		Name:        "p",
		Namespace:   "default",
		UID:         "u1",
		Annotations: map[string]string{podAnnotationKernelBootMode: string(kernel.BootModeSnapshot)},
	})
	if err != nil {
		t.Fatalf("run pod: %v", err)
	}

	if _, err := e.CreateContainer(ctx, ContainerSpec{PodID: pod.ID, Name: "c", Image: "busybox"}); err != nil {
		t.Fatalf("create container: %v", err)
	}

	if client.forceReadyCalls != 1 {
		t.Fatalf("expected 1 force-ready call, got %d", client.forceReadyCalls)
	}
}

func TestCreateContainerForColdBootPodDoesNotForcePeerReady(t *testing.T) {
	client := &readyClient{}
	e, _, _ := newTestEngineWithFactory(t, &readyFactory{client: client})
	ctx := context.Background()

	pod, err := e.RunPod(ctx, PodSpec{
		Name:      "p",
		Namespace: "default",
		UID:       "u1",
	})
	if err != nil {
		t.Fatalf("run pod: %v", err)
	}

	if _, err := e.CreateContainer(ctx, ContainerSpec{PodID: pod.ID, Name: "c", Image: "busybox"}); err != nil {
		t.Fatalf("create container: %v", err)
	}

	if client.forceReadyCalls != 0 {
		t.Fatalf("expected 0 force-ready calls, got %d", client.forceReadyCalls)
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

func TestRunPodSkipsWaitReadyWhenKernelInstanceRequestsIt(t *testing.T) {
	client := &readyClient{}
	e, km, _ := newTestEngineWithFactory(t, &readyFactory{client: client})

	if _, err := e.RunPod(context.Background(), PodSpec{
		Name:        "p",
		Namespace:   "default",
		UID:         "u1",
		Annotations: map[string]string{podAnnotationKernelBootMode: string(kernel.BootModeSnapshot)},
	}); err != nil {
		t.Fatalf("run pod: %v", err)
	}

	if client.waitCalls != 0 {
		t.Fatalf("expected wait ready to be skipped, got %d calls", client.waitCalls)
	}
	if km.lastStartRequest.BootMode != kernel.BootModeSnapshot {
		t.Fatalf("expected snapshot boot mode, got %q", km.lastStartRequest.BootMode)
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

func TestStopContainerBlocksAutoCleanupUntilStopReturns(t *testing.T) {
	client := &readyClient{
		stopStart: make(chan struct{}, 1),
		stopBlock: make(chan struct{}),
	}
	e, km, _ := newTestEngineWithFactory(t, &readyFactory{client: client})
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

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- e.StopContainer(ctx, ctr.ID, 2*time.Second)
	}()

	select {
	case <-client.stopStart:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for StopContainer to reach agent")
	}

	time.Sleep(700 * time.Millisecond)
	if len(km.stopCalls) != 0 {
		t.Fatalf("expected auto cleanup to stay blocked while StopContainer is in flight, got %d stop kernel calls", len(km.stopCalls))
	}

	close(client.stopBlock)

	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("stop container: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for StopContainer to return")
	}

	waitForCondition(t, 2*time.Second, func() bool {
		return len(km.stopCalls) == 1
	})
}

func TestCleanupExitedContainerSkipsCanceledContext(t *testing.T) {
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

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if cleaned := e.cleanupExitedContainer(cancelCtx, ctr.ID); cleaned {
		t.Fatalf("expected canceled cleanup to return false")
	}
	if len(km.stopCalls) != 0 {
		t.Fatalf("expected canceled cleanup to skip StopPod, got %d stop kernel calls", len(km.stopCalls))
	}
}

func TestStopContainerLeavesResponseBudgetBeforeContextDeadline(t *testing.T) {
	client := &readyClient{}
	e, _, _ := newTestEngineWithFactory(t, &readyFactory{client: client})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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

	if err := e.StopContainer(ctx, ctr.ID, 30*time.Second); err != nil {
		t.Fatalf("stop container: %v", err)
	}

	client.mu.Lock()
	got := client.stopTimeout
	client.mu.Unlock()

	if got <= 0 {
		t.Fatalf("expected forwarded stop timeout to be positive")
	}
	if got >= 30*time.Second {
		t.Fatalf("expected forwarded stop timeout to leave response budget, got %v", got)
	}
}
