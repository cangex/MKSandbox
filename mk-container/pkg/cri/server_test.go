package cri

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"mk-container/pkg/agent"
	"mk-container/pkg/kernel"
	mkrt "mk-container/pkg/runtime"
	"mk-container/pkg/streaming"
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
	lastCreate   agent.ContainerSpec
	lastStop     struct {
		containerID string
		timeout     time.Duration
	}
	lastExecPrepare agent.ExecTTYRequest
}

func (c *countingClient) WaitReady(_ context.Context) error {
	return nil
}

func (c *countingClient) CreateContainer(_ context.Context, spec agent.ContainerSpec) (string, string, error) {
	c.lastCreate = spec
	return "ctr-test", "", nil
}

func (c *countingClient) StartContainer(_ context.Context, _ string) error {
	return nil
}

func (c *countingClient) StopContainer(_ context.Context, containerID string, timeout time.Duration) (int32, error) {
	c.lastStop.containerID = containerID
	c.lastStop.timeout = timeout
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

func (c *countingClient) ExecTTYPrepare(_ context.Context, req agent.ExecTTYRequest) (*agent.ExecTTYPrepareResult, error) {
	c.lastExecPrepare = req
	return &agent.ExecTTYPrepareResult{SessionID: "exec-session-test"}, nil
}

func (c *countingClient) ExecTTYStart(_ context.Context, _ agent.ExecTTYStartRequest) error {
	return agent.ErrNotImplemented
}

func (c *countingClient) ExecTTYResize(_ context.Context, _ agent.ExecTTYResizeRequest) error {
	return agent.ErrNotImplemented
}

func (c *countingClient) ExecTTYClose(_ context.Context, _ agent.ExecTTYCloseRequest) error {
	return agent.ErrNotImplemented
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
	return NewServer(engine, "mkcri", "test", streaming.NewServer(nil, "http://127.0.0.1:10010"))
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

func TestCreateContainerPassesCommandAndArgs(t *testing.T) {
	client := &countingClient{}
	server := newCRITestServer(t, client)
	ctx := context.Background()

	runResp, err := server.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
		Config: &runtimeapi.PodSandboxConfig{
			Metadata: &runtimeapi.PodSandboxMetadata{
				Name:      "p",
				Namespace: "default",
				Uid:       "u2",
				Attempt:   1,
			},
		},
	})
	if err != nil {
		t.Fatalf("run pod sandbox: %v", err)
	}

	_, err = server.CreateContainer(ctx, &runtimeapi.CreateContainerRequest{
		PodSandboxId: runResp.PodSandboxId,
		Config: &runtimeapi.ContainerConfig{
			Metadata: &runtimeapi.ContainerMetadata{
				Name:    "logger",
				Attempt: 1,
			},
			Image:   &runtimeapi.ImageSpec{Image: "busybox"},
			Command: []string{"sh", "-c"},
			Args:    []string{"while true; do echo tick; sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}

	if got, want := len(client.lastCreate.Command), 2; got != want {
		t.Fatalf("unexpected command length: got=%d want=%d", got, want)
	}
	if client.lastCreate.Command[0] != "sh" || client.lastCreate.Command[1] != "-c" {
		t.Fatalf("unexpected command: %#v", client.lastCreate.Command)
	}
	if got, want := len(client.lastCreate.Args), 1; got != want {
		t.Fatalf("unexpected args length: got=%d want=%d", got, want)
	}
	if client.lastCreate.Args[0] != "while true; do echo tick; sleep 1; done" {
		t.Fatalf("unexpected args: %#v", client.lastCreate.Args)
	}
}

func TestStopContainerUsesDefaultTimeoutWhenUnset(t *testing.T) {
	client := &countingClient{}
	server := newCRITestServer(t, client)
	ctx := context.Background()

	runResp, err := server.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
		Config: &runtimeapi.PodSandboxConfig{
			Metadata: &runtimeapi.PodSandboxMetadata{
				Name:      "p",
				Namespace: "default",
				Uid:       "u3",
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

	if _, err := server.StopContainer(ctx, &runtimeapi.StopContainerRequest{
		ContainerId: createResp.ContainerId,
	}); err != nil {
		t.Fatalf("stop container: %v", err)
	}

	if got, want := client.lastStop.containerID, createResp.ContainerId; got != want {
		t.Fatalf("unexpected stop container id: got=%q want=%q", got, want)
	}
	if got, want := client.lastStop.timeout, defaultStopContainerTimeout; got != want {
		t.Fatalf("unexpected default stop timeout: got=%s want=%s", got, want)
	}
}

func TestExecReturnsStreamingURL(t *testing.T) {
	client := &countingClient{}
	server := newCRITestServer(t, client)
	ctx := context.Background()

	runResp, err := server.RunPodSandbox(ctx, &runtimeapi.RunPodSandboxRequest{
		Config: &runtimeapi.PodSandboxConfig{
			Metadata: &runtimeapi.PodSandboxMetadata{
				Name:      "p",
				Namespace: "default",
				Uid:       "u4",
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

	if _, err := server.StartContainer(ctx, &runtimeapi.StartContainerRequest{
		ContainerId: createResp.ContainerId,
	}); err != nil {
		t.Fatalf("start container: %v", err)
	}

	resp, err := server.Exec(ctx, &runtimeapi.ExecRequest{
		ContainerId: createResp.ContainerId,
		Cmd:         []string{"sh"},
		Tty:         true,
		Stdin:       true,
		Stdout:      true,
	})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.HasPrefix(resp.GetUrl(), "http://127.0.0.1:10010/exec/") {
		t.Fatalf("unexpected exec url: %q", resp.GetUrl())
	}
	if client.lastExecPrepare.ContainerID != createResp.ContainerId {
		t.Fatalf("unexpected exec prepare container id: got=%q want=%q", client.lastExecPrepare.ContainerID, createResp.ContainerId)
	}
	if len(client.lastExecPrepare.Command) != 1 || client.lastExecPrepare.Command[0] != "sh" {
		t.Fatalf("unexpected exec command: %#v", client.lastExecPrepare.Command)
	}
}
