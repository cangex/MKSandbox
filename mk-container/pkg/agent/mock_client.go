package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"mk-container/pkg/util"
)

type containerStatus struct {
	created bool
	running bool
}

type mockClient struct {
	mu         sync.Mutex
	containers map[string]containerStatus
}

func (c *mockClient) WaitReady(_ context.Context) error {
	return nil
}

func (c *mockClient) ConfigureNetwork(_ context.Context, _ NetworkSpec) error {
	return nil
}

func (c *mockClient) ConfigureContainerEnv(_ context.Context, _, _ string, _ []EnvVar) error {
	return nil
}

func (c *mockClient) CreateContainer(_ context.Context, _ ContainerSpec) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := util.NewID()
	c.containers[id] = containerStatus{created: true}
	return id, "", nil
}

func (c *mockClient) StartContainer(_ context.Context, containerID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	st, ok := c.containers[containerID]
	if !ok || !st.created {
		return fmt.Errorf("container %s not found", containerID)
	}
	st.running = true
	c.containers[containerID] = st
	return nil
}

func (c *mockClient) StopContainer(_ context.Context, containerID string, _ time.Duration) (int32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	st, ok := c.containers[containerID]
	if !ok {
		return 0, fmt.Errorf("container %s not found", containerID)
	}
	st.running = false
	c.containers[containerID] = st
	return 0, nil
}

func (c *mockClient) RemoveContainer(_ context.Context, containerID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.containers, containerID)
	return nil
}

func (c *mockClient) ContainerStatus(_ context.Context, containerID string) (*ContainerStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	st, ok := c.containers[containerID]
	if !ok {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	state := "CREATED"
	if st.running {
		state = "RUNNING"
	}
	return &ContainerStatus{
		State:    state,
		ExitCode: 0,
	}, nil
}

func (c *mockClient) ReadLog(_ context.Context, containerID string, offset uint64, maxBytes int) (*LogChunk, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = maxBytes

	if _, ok := c.containers[containerID]; !ok {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	return &LogChunk{
		Data:       nil,
		NextOffset: offset,
		EOF:        true,
	}, nil
}

func (c *mockClient) ExecTTYPrepare(_ context.Context, _ ExecTTYRequest) (*ExecTTYPrepareResult, error) {
	return nil, ErrNotImplemented
}

func (c *mockClient) ExecTTYStart(_ context.Context, _ ExecTTYStartRequest) error {
	return ErrNotImplemented
}

func (c *mockClient) ExecTTYResize(_ context.Context, _ ExecTTYResizeRequest) error {
	return ErrNotImplemented
}

func (c *mockClient) ExecTTYClose(_ context.Context, _ ExecTTYCloseRequest) error {
	return ErrNotImplemented
}

type MockFactory struct {
	mu      sync.Mutex
	clients map[string]*mockClient
}

func NewMockFactory() *MockFactory {
	return &MockFactory{clients: map[string]*mockClient{}}
}

func (f *MockFactory) ForKernel(kernelID, _ string) Client {
	f.mu.Lock()
	defer f.mu.Unlock()

	if c, ok := f.clients[kernelID]; ok {
		return c
	}

	c := &mockClient{containers: map[string]containerStatus{}}
	f.clients[kernelID] = c
	return c
}
