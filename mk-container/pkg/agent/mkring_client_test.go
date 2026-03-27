package agent

import (
	"context"
	"testing"
	"time"

	"mk-container/pkg/transport/mkringcontrol"
)

func TestNewMkringClientParsesEndpoint(t *testing.T) {
	svc := mkringcontrol.New(mkringcontrol.NewStubTransport())
	client, err := newMkringClient("kernel-a", "mkring://7?kernel_id=abc", svc)
	if err != nil {
		t.Fatalf("new direct client: %v", err)
	}

	direct, ok := client.(*mkringClient)
	if !ok {
		t.Fatalf("unexpected client type: %T", client)
	}
	if direct.kernelID != "kernel-a" {
		t.Fatalf("unexpected kernel id: %q", direct.kernelID)
	}
	if direct.peerKernelID != 7 {
		t.Fatalf("unexpected peer id: %d", direct.peerKernelID)
	}
}

func TestMkringClientLifecycle(t *testing.T) {
	svc := mkringcontrol.New(mkringcontrol.NewStubTransport())
	client, err := newMkringClient("kernel-a", "mkring://7", svc)
	if err != nil {
		t.Fatalf("new direct client: %v", err)
	}

	direct := client.(*mkringClient)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := direct.WaitReady(ctx); err != nil {
		t.Fatalf("wait ready: %v", err)
	}

	if err := direct.ForcePeerReady(ctx); err != nil {
		t.Fatalf("force peer ready: %v", err)
	}

	containerID, imageRef, err := direct.CreateContainer(ctx, ContainerSpec{
		PodID: "pod-1",
		Name:  "ctr",
		Image: "img",
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	if containerID == "" {
		t.Fatalf("empty container id")
	}
	if imageRef == "" {
		t.Fatalf("empty image ref")
	}

	if err := direct.StartContainer(ctx, containerID); err != nil {
		t.Fatalf("start container: %v", err)
	}

	exitCode, err := direct.StopContainer(ctx, containerID, 2*time.Second)
	if err != nil {
		t.Fatalf("stop container: %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("unexpected exit code: %d", exitCode)
	}

	if err := direct.RemoveContainer(ctx, containerID); err != nil {
		t.Fatalf("remove container: %v", err)
	}

	prepare, err := direct.ExecTTYPrepare(ctx, ExecTTYRequest{
		ContainerID: containerID,
		Command:     []string{"sh"},
		TTY:         true,
		Stdin:       true,
		Stdout:      true,
	})
	if err != nil {
		t.Fatalf("exec tty prepare: %v", err)
	}
	if prepare.SessionID == "" {
		t.Fatalf("empty session id")
	}

	if err := direct.ExecTTYStart(ctx, ExecTTYStartRequest{SessionID: prepare.SessionID}); err != nil {
		t.Fatalf("exec tty start: %v", err)
	}
	if err := direct.ExecTTYResize(ctx, ExecTTYResizeRequest{SessionID: prepare.SessionID, Width: 120, Height: 40}); err != nil {
		t.Fatalf("exec tty resize: %v", err)
	}
	if err := direct.ExecTTYClose(ctx, ExecTTYCloseRequest{SessionID: prepare.SessionID}); err != nil {
		t.Fatalf("exec tty close: %v", err)
	}
}

func TestMkringFactoryReturnsErrorClientForBadEndpoint(t *testing.T) {
	factory := NewMkringFactory("/dev/mkring_container_bridge")
	client := factory.ForKernel("kernel-a", "http://bad")

	if err := client.WaitReady(context.Background()); err == nil {
		t.Fatalf("expected wait ready error")
	}
}
