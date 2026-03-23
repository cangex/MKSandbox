package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mk-container/pkg/agent"
	"mk-container/pkg/kernel"
	"mk-container/pkg/model"
	"mk-container/pkg/util"
)

type PodSpec struct {
	Name           string
	Namespace      string
	UID            string
	Attempt        uint32
	Labels         map[string]string
	Annotations    map[string]string
	RuntimeHandler string
}

type ContainerSpec struct {
	PodID       string
	Name        string
	Attempt     uint32
	Image       string
	Command     []string
	Args        []string
	Labels      map[string]string
	Annotations map[string]string
	LogPath     string
}

type Engine struct {
	mu sync.RWMutex

	kernels        map[string]*model.Kernel
	pods           map[string]*model.Pod
	containers     map[string]*model.Container
	podToContainer map[string]string
	podCleanup     map[string]struct{}
	podCleanupCond *sync.Cond
	logSync        map[string]*containerLogSync
	monitors       map[string]context.CancelFunc
	kernelManager  kernel.Manager
	agentFactory   agent.Factory
	podIPAllocator *util.IPAllocator
	kernelIDAlloc  *util.IntAllocator
}

type containerLogSync struct {
	offset uint64
	eof    bool
}

func NewEngine(km kernel.Manager, af agent.Factory, allocator *util.IPAllocator, kernelIDAlloc *util.IntAllocator) *Engine {
	e := &Engine{
		kernels:        map[string]*model.Kernel{},
		pods:           map[string]*model.Pod{},
		containers:     map[string]*model.Container{},
		podToContainer: map[string]string{},
		podCleanup:     map[string]struct{}{},
		logSync:        map[string]*containerLogSync{},
		monitors:       map[string]context.CancelFunc{},
		kernelManager:  km,
		agentFactory:   af,
		podIPAllocator: allocator,
		kernelIDAlloc:  kernelIDAlloc,
	}
	e.podCleanupCond = sync.NewCond(&e.mu)
	return e
}

func copyMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copySlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func (e *Engine) waitForPodCleanupLocked(podID string) {
	for {
		if _, busy := e.podCleanup[podID]; !busy {
			return
		}
		e.podCleanupCond.Wait()
	}
}

func (e *Engine) beginPodCleanupLocked(podID string) {
	e.podCleanup[podID] = struct{}{}
}

func (e *Engine) endPodCleanupLocked(podID string) {
	delete(e.podCleanup, podID)
	e.podCleanupCond.Broadcast()
}

func (e *Engine) syncLogState(containerID string) *containerLogSync {
	if st, ok := e.logSync[containerID]; ok {
		return st
	}
	st := &containerLogSync{}
	e.logSync[containerID] = st
	return st
}

func (e *Engine) stopContainerMonitorLocked(containerID string) {
	if cancel, ok := e.monitors[containerID]; ok {
		cancel()
		delete(e.monitors, containerID)
	}
}

func (e *Engine) logEOF(containerID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if st, ok := e.logSync[containerID]; ok {
		return st.eof
	}
	return false
}

func (e *Engine) getContainerState(containerID string) (model.ContainerState, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	ctr, ok := e.containers[containerID]
	if !ok {
		return "", false
	}
	return ctr.State, true
}

func (e *Engine) syncContainerOnce(ctx context.Context, containerID string) bool {
	if err := e.SyncContainerLogs(ctx, containerID); err != nil {
		if _, ok := e.GetContainer(containerID); !ok {
			return true
		}
	}

	if _, err := e.RefreshContainerStatus(ctx, containerID); err != nil {
		if _, ok := e.GetContainer(containerID); !ok {
			return true
		}
	}

	state, ok := e.getContainerState(containerID)
	if !ok {
		return true
	}
	return state == model.ContainerStateExited && e.logEOF(containerID)
}

func (e *Engine) cleanupExitedContainer(ctx context.Context, containerID string) bool {
	e.mu.RLock()
	ctr, ok := e.containers[containerID]
	if !ok {
		e.mu.RUnlock()
		return true
	}
	podID := ctr.PodID
	e.mu.RUnlock()

	if err := e.StopPod(ctx, podID); err != nil {
		if _, ok := e.GetContainer(containerID); !ok {
			return true
		}
		return false
	}
	return true
}

func (e *Engine) monitorContainer(ctx context.Context, containerID string) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	if e.syncContainerOnce(ctx, containerID) {
		if e.cleanupExitedContainer(ctx, containerID) {
			e.mu.Lock()
			delete(e.monitors, containerID)
			e.mu.Unlock()
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.syncContainerOnce(ctx, containerID) {
				if e.cleanupExitedContainer(ctx, containerID) {
					e.mu.Lock()
					delete(e.monitors, containerID)
					e.mu.Unlock()
					return
				}
			}
		}
	}
}

func (e *Engine) startContainerMonitor(containerID string) {
	e.mu.Lock()
	e.stopContainerMonitorLocked(containerID)
	ctx, cancel := context.WithCancel(context.Background())
	e.monitors[containerID] = cancel
	e.mu.Unlock()

	go e.monitorContainer(ctx, containerID)
}

func (e *Engine) RunPod(ctx context.Context, spec PodSpec) (_ *model.Pod, err error) {
	podID := util.NewID()
	kernelID := util.NewID()
	peerKernelID, err := e.kernelIDAlloc.Allocate()
	if err != nil {
		return nil, err
	}

	instance, err := e.kernelManager.StartKernel(ctx, kernel.StartRequest{
		KernelID:     kernelID,
		PeerKernelID: uint16(peerKernelID),
		PodID:        podID,
		Namespace:    spec.Namespace,
		Name:         spec.Name,
	})
	if err != nil {
		e.kernelIDAlloc.Release(peerKernelID)
		return nil, err
	}

	if err := e.agentFactory.ForKernel(kernelID, instance.Endpoint).WaitReady(ctx); err != nil {
		_ = e.kernelManager.StopKernel(context.Background(), kernelID)
		e.kernelIDAlloc.Release(peerKernelID)
		return nil, fmt.Errorf("wait for guest ready: %w", err)
	}

	ip, err := e.podIPAllocator.Allocate()
	if err != nil {
		_ = e.kernelManager.StopKernel(context.Background(), kernelID)
		e.kernelIDAlloc.Release(peerKernelID)
		return nil, err
	}
	defer func() {
		if err != nil {
			e.podIPAllocator.Release(ip)
		}
	}()

	now := time.Now().UTC()
	pod := &model.Pod{
		ID:             podID,
		Name:           spec.Name,
		Namespace:      spec.Namespace,
		UID:            spec.UID,
		Attempt:        spec.Attempt,
		CreatedAt:      now,
		State:          model.PodStateReady,
		Labels:         copyMap(spec.Labels),
		Annotations:    copyMap(spec.Annotations),
		KernelID:       kernelID,
		RuntimeHandler: spec.RuntimeHandler,
		IP:             ip,
	}

	e.mu.Lock()
	e.kernels[kernelID] = &model.Kernel{
		ID:           kernelID,
		PeerKernelID: instance.PeerKernelID,
		Endpoint:     instance.Endpoint,
		StartedAt:    now,
	}
	e.pods[podID] = pod
	e.mu.Unlock()

	cp := *pod
	return &cp, nil
}

func (e *Engine) StopPod(ctx context.Context, podID string) error {
	e.mu.Lock()
	e.waitForPodCleanupLocked(podID)
	pod, ok := e.pods[podID]
	if !ok {
		e.mu.Unlock()
		return nil
	}
	containerID, hasContainer := e.podToContainer[podID]
	var ctr *model.Container
	if hasContainer {
		ctr = e.containers[containerID]
	}
	kernelID := pod.KernelID
	podIP := pod.IP
	shouldStopKernel := e.kernels[kernelID] != nil
	if !shouldStopKernel && pod.State == model.PodStateNotReady && podIP == "" {
		e.mu.Unlock()
		return nil
	}
	e.beginPodCleanupLocked(podID)
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.endPodCleanupLocked(podID)
		e.mu.Unlock()
	}()

	if ctr != nil && ctr.State == model.ContainerStateRunning {
		if err := e.StopContainer(ctx, ctr.ID, 10*time.Second); err != nil {
			return err
		}
	}

	if shouldStopKernel {
		if err := e.kernelManager.StopKernel(ctx, kernelID); err != nil {
			return err
		}
	}

	e.mu.Lock()
	if p := e.pods[podID]; p != nil {
		p.State = model.PodStateNotReady
		p.IP = ""
	}
	if kernelObj := e.kernels[kernelID]; kernelObj != nil {
		delete(e.kernels, kernelObj.ID)
		e.kernelIDAlloc.Release(int(kernelObj.PeerKernelID))
	}
	e.mu.Unlock()
	if podIP != "" {
		e.podIPAllocator.Release(podIP)
	}
	return nil
}

func (e *Engine) RemovePod(ctx context.Context, podID string) error {
	e.mu.Lock()
	e.waitForPodCleanupLocked(podID)
	pod, ok := e.pods[podID]
	if !ok {
		e.mu.Unlock()
		return nil
	}
	containerID, hasContainer := e.podToContainer[podID]
	kernelID := pod.KernelID
	podIP := pod.IP
	shouldStopKernel := e.kernels[kernelID] != nil
	e.beginPodCleanupLocked(podID)
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.endPodCleanupLocked(podID)
		e.mu.Unlock()
	}()

	if hasContainer {
		if err := e.RemoveContainer(ctx, containerID); err != nil {
			return err
		}
	}

	if shouldStopKernel {
		_ = e.kernelManager.StopKernel(ctx, kernelID)
	}

	e.mu.Lock()
	if kernelObj := e.kernels[kernelID]; kernelObj != nil {
		delete(e.kernels, kernelID)
		e.kernelIDAlloc.Release(int(kernelObj.PeerKernelID))
	}
	delete(e.podToContainer, podID)
	delete(e.pods, podID)
	e.mu.Unlock()
	if podIP != "" {
		e.podIPAllocator.Release(podIP)
	}
	return nil
}

func (e *Engine) CreateContainer(ctx context.Context, spec ContainerSpec) (*model.Container, error) {
	e.mu.RLock()
	pod, ok := e.pods[spec.PodID]
	if !ok {
		e.mu.RUnlock()
		return nil, fmt.Errorf("pod %s not found", spec.PodID)
	}
	if _, exists := e.podToContainer[spec.PodID]; exists {
		e.mu.RUnlock()
		return nil, fmt.Errorf("pod %s already has a container (1 kernel = 1 container)", spec.PodID)
	}
	kernelObj := e.kernels[pod.KernelID]
	e.mu.RUnlock()

	if kernelObj == nil {
		return nil, fmt.Errorf("kernel %s not found", pod.KernelID)
	}

	client := e.agentFactory.ForKernel(kernelObj.ID, kernelObj.Endpoint)
	containerID, imageRef, err := client.CreateContainer(ctx, agent.ContainerSpec{
		PodID:       spec.PodID,
		Name:        spec.Name,
		Image:       spec.Image,
		Command:     copySlice(spec.Command),
		Args:        copySlice(spec.Args),
		Labels:      copyMap(spec.Labels),
		Annotations: copyMap(spec.Annotations),
		LogPath:     spec.LogPath,
	})
	if err != nil {
		return nil, err
	}
	if containerID == "" {
		containerID = util.NewID()
	}

	now := time.Now().UTC()
	ctr := &model.Container{
		ID:          containerID,
		PodID:       spec.PodID,
		Name:        spec.Name,
		Attempt:     spec.Attempt,
		CreatedAt:   now,
		State:       model.ContainerStateCreated,
		Image:       spec.Image,
		ImageRef:    imageRef,
		Labels:      copyMap(spec.Labels),
		Annotations: copyMap(spec.Annotations),
		LogPath:     spec.LogPath,
	}

	e.mu.Lock()
	e.containers[containerID] = ctr
	e.podToContainer[spec.PodID] = containerID
	e.syncLogState(containerID)
	e.mu.Unlock()

	cp := *ctr
	return &cp, nil
}

func (e *Engine) StartContainer(ctx context.Context, containerID string) error {
	e.mu.RLock()
	ctr, ok := e.containers[containerID]
	if !ok {
		e.mu.RUnlock()
		return fmt.Errorf("container %s not found", containerID)
	}
	pod := e.pods[ctr.PodID]
	if pod == nil {
		e.mu.RUnlock()
		return fmt.Errorf("pod %s for container %s not found", ctr.PodID, containerID)
	}
	kernelObj := e.kernels[pod.KernelID]
	e.mu.RUnlock()

	if kernelObj == nil {
		return fmt.Errorf("kernel for container %s not found", containerID)
	}

	if err := e.agentFactory.ForKernel(kernelObj.ID, kernelObj.Endpoint).StartContainer(ctx, containerID); err != nil {
		return err
	}

	e.mu.Lock()
	if c := e.containers[containerID]; c != nil {
		c.State = model.ContainerStateRunning
		c.StartedAt = time.Now().UTC()
	}
	e.mu.Unlock()
	e.startContainerMonitor(containerID)
	return nil
}

func (e *Engine) SyncContainerLogs(ctx context.Context, containerID string) error {
	e.mu.RLock()
	ctr, ok := e.containers[containerID]
	if !ok {
		e.mu.RUnlock()
		return fmt.Errorf("container %s not found", containerID)
	}
	if ctr.LogPath == "" {
		e.mu.RUnlock()
		return nil
	}
	pod := e.pods[ctr.PodID]
	if pod == nil {
		e.mu.RUnlock()
		return fmt.Errorf("pod %s for container %s not found", ctr.PodID, containerID)
	}
	kernelObj := e.kernels[pod.KernelID]
	if kernelObj == nil {
		e.mu.RUnlock()
		return fmt.Errorf("kernel for container %s not found", containerID)
	}
	logPath := ctr.LogPath
	offset := uint64(0)
	eof := false
	if st, ok := e.logSync[containerID]; ok {
		offset = st.offset
		eof = st.eof
	}
	e.mu.RUnlock()

	if eof {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	client := e.agentFactory.ForKernel(kernelObj.ID, kernelObj.Endpoint)
	currentOffset := offset
	currentEOF := false

	for i := 0; i < 128; i++ {
		chunk, err := client.ReadLog(ctx, containerID, currentOffset, 384)
		if err != nil {
			return err
		}
		if chunk == nil {
			break
		}
		if len(chunk.Data) > 0 {
			if _, err := file.Write(chunk.Data); err != nil {
				return err
			}
		}
		currentEOF = chunk.EOF

		advanced := chunk.NextOffset > currentOffset
		currentOffset = chunk.NextOffset

		if !advanced || len(chunk.Data) == 0 {
			break
		}
		if currentEOF {
			break
		}
	}

	e.mu.Lock()
	if _, ok := e.containers[containerID]; ok {
		st := e.syncLogState(containerID)
		st.offset = currentOffset
		st.eof = currentEOF
	}
	e.mu.Unlock()
	return nil
}

func statusStringToModel(state string) model.ContainerState {
	switch state {
	case "CREATED":
		return model.ContainerStateCreated
	case "RUNNING":
		return model.ContainerStateRunning
	case "EXITED":
		return model.ContainerStateExited
	default:
		return ""
	}
}

func (e *Engine) RefreshContainerStatus(ctx context.Context, containerID string) (*model.Container, error) {
	e.mu.RLock()
	ctr, ok := e.containers[containerID]
	if !ok {
		e.mu.RUnlock()
		return nil, fmt.Errorf("container %s not found", containerID)
	}
	pod := e.pods[ctr.PodID]
	if pod == nil {
		e.mu.RUnlock()
		return nil, fmt.Errorf("pod %s for container %s not found", ctr.PodID, containerID)
	}
	kernelObj := e.kernels[pod.KernelID]
	e.mu.RUnlock()

	if kernelObj == nil {
		return nil, fmt.Errorf("kernel for container %s not found", containerID)
	}

	remoteStatus, err := e.agentFactory.ForKernel(kernelObj.ID, kernelObj.Endpoint).ContainerStatus(ctx, containerID)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	current := e.containers[containerID]
	if current == nil {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	if mapped := statusStringToModel(remoteStatus.State); mapped != "" {
		current.State = mapped
	}
	current.ExitCode = remoteStatus.ExitCode
	if !remoteStatus.StartedAt.IsZero() {
		current.StartedAt = remoteStatus.StartedAt
	}
	if !remoteStatus.FinishedAt.IsZero() {
		current.FinishedAt = remoteStatus.FinishedAt
	}
	current.Message = remoteStatus.Message
	if current.State == model.ContainerStateExited && current.Reason == "" {
		current.Reason = "Completed"
	}

	cp := *current
	return &cp, nil
}

func (e *Engine) StopContainer(ctx context.Context, containerID string, timeout time.Duration) error {
	e.mu.RLock()
	ctr, ok := e.containers[containerID]
	if !ok {
		e.mu.RUnlock()
		return nil
	}
	pod := e.pods[ctr.PodID]
	if pod == nil {
		e.mu.RUnlock()
		return fmt.Errorf("pod %s for container %s not found", ctr.PodID, containerID)
	}
	kernelObj := e.kernels[pod.KernelID]
	e.mu.RUnlock()

	if kernelObj == nil {
		return fmt.Errorf("kernel for container %s not found", containerID)
	}

	exitCode, err := e.agentFactory.ForKernel(kernelObj.ID, kernelObj.Endpoint).StopContainer(ctx, containerID, timeout)
	if err != nil {
		return err
	}

	e.mu.Lock()
	if c := e.containers[containerID]; c != nil {
		c.State = model.ContainerStateExited
		c.ExitCode = exitCode
		c.FinishedAt = time.Now().UTC()
	}
	e.mu.Unlock()
	return nil
}

func (e *Engine) RemoveContainer(ctx context.Context, containerID string) error {
	e.mu.RLock()
	ctr, ok := e.containers[containerID]
	if !ok {
		e.mu.RUnlock()
		return nil
	}
	pod := e.pods[ctr.PodID]
	if pod == nil {
		e.mu.RUnlock()
		e.mu.Lock()
		delete(e.containers, containerID)
		delete(e.podToContainer, ctr.PodID)
		e.mu.Unlock()
		return nil
	}
	kernelObj := e.kernels[pod.KernelID]
	e.mu.RUnlock()

	if kernelObj != nil {
		_ = e.agentFactory.ForKernel(kernelObj.ID, kernelObj.Endpoint).RemoveContainer(ctx, containerID)
	}

	e.mu.Lock()
	e.stopContainerMonitorLocked(containerID)
	delete(e.containers, containerID)
	delete(e.podToContainer, ctr.PodID)
	delete(e.logSync, containerID)
	e.mu.Unlock()
	return nil
}

func (e *Engine) GetPod(podID string) (*model.Pod, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	pod, ok := e.pods[podID]
	if !ok {
		return nil, false
	}
	cp := *pod
	return &cp, true
}

func (e *Engine) ListPods() []*model.Pod {
	e.mu.RLock()
	defer e.mu.RUnlock()
	items := make([]*model.Pod, 0, len(e.pods))
	for _, pod := range e.pods {
		cp := *pod
		items = append(items, &cp)
	}
	return items
}

func (e *Engine) GetContainer(containerID string) (*model.Container, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ctr, ok := e.containers[containerID]
	if !ok {
		return nil, false
	}
	cp := *ctr
	return &cp, true
}

func (e *Engine) ListContainers() []*model.Container {
	e.mu.RLock()
	defer e.mu.RUnlock()
	items := make([]*model.Container, 0, len(e.containers))
	for _, ctr := range e.containers {
		cp := *ctr
		items = append(items, &cp)
	}
	return items
}
