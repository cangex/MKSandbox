package agent

import (
	"context"
	"time"

	"mk-container/pkg/transport/mkringcontrol"
)

type mkringClient struct {
	kernelID     string
	peerKernelID uint16
	service      *mkringcontrol.Service
}

func newMkringClient(kernelID, endpoint string, svc *mkringcontrol.Service) (Client, error) {
	peerKernelID, err := parseMkringEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	return &mkringClient{
		kernelID:     kernelID,
		peerKernelID: peerKernelID,
		service:      svc,
	}, nil
}

func (c *mkringClient) WaitReady(ctx context.Context) error {
	return c.service.WaitReady(ctx, c.peerKernelID, c.kernelID, 0)
}

func (c *mkringClient) ForcePeerReady(ctx context.Context) error {
	return c.service.ForcePeerReady(ctx, c.peerKernelID, c.kernelID)
}

func (c *mkringClient) CreateContainer(ctx context.Context, spec ContainerSpec) (string, string, error) {
	result, err := c.service.CreateContainer(ctx, c.peerKernelID, mkringcontrol.CreateContainerPayload{
		KernelID:    c.kernelID,
		PodID:       spec.PodID,
		Name:        spec.Name,
		Image:       spec.Image,
		Command:     append([]string(nil), spec.Command...),
		Args:        append([]string(nil), spec.Args...),
		Labels:      copyStringMap(spec.Labels),
		Annotations: copyStringMap(spec.Annotations),
		LogPath:     spec.LogPath,
	})
	if err != nil {
		return "", "", err
	}
	return result.ContainerID, result.ImageRef, nil
}

func (c *mkringClient) StartContainer(ctx context.Context, containerID string) error {
	return c.service.StartContainer(ctx, c.peerKernelID, mkringcontrol.ContainerControlPayload{
		KernelID:    c.kernelID,
		ContainerID: containerID,
	})
}

func (c *mkringClient) StopContainer(ctx context.Context, containerID string, timeout time.Duration) (int32, error) {
	result, err := c.service.StopContainer(ctx, c.peerKernelID, mkringcontrol.ContainerControlPayload{
		KernelID:      c.kernelID,
		ContainerID:   containerID,
		TimeoutMillis: timeout.Milliseconds(),
	})
	if err != nil {
		return 0, err
	}
	return result.ExitCode, nil
}

func (c *mkringClient) RemoveContainer(ctx context.Context, containerID string) error {
	return c.service.RemoveContainer(ctx, c.peerKernelID, mkringcontrol.ContainerControlPayload{
		KernelID:    c.kernelID,
		ContainerID: containerID,
	})
}

func (c *mkringClient) ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error) {
	result, err := c.service.ContainerStatus(ctx, c.peerKernelID, mkringcontrol.ContainerControlPayload{
		KernelID:    c.kernelID,
		ContainerID: containerID,
	})
	if err != nil {
		return nil, err
	}

	status := &ContainerStatus{
		State:    string(result.State),
		ExitCode: result.ExitCode,
		PID:      result.PID,
		Message:  result.Message,
	}
	if result.StartedAtUnixNano != 0 {
		status.StartedAt = time.Unix(0, int64(result.StartedAtUnixNano)).UTC()
	}
	if result.FinishedAtUnixNano != 0 {
		status.FinishedAt = time.Unix(0, int64(result.FinishedAtUnixNano)).UTC()
	}
	return status, nil
}

func (c *mkringClient) ReadLog(ctx context.Context, containerID string, offset uint64, maxBytes int) (*LogChunk, error) {
	payload := mkringcontrol.ReadLogPayload{
		KernelID:    c.kernelID,
		ContainerID: containerID,
		Offset:      offset,
	}
	if maxBytes > 0 {
		payload.MaxBytes = uint32(maxBytes)
	}

	result, err := c.service.ReadLog(ctx, c.peerKernelID, payload)
	if err != nil {
		return nil, err
	}
	return &LogChunk{
		Data:       result.Data,
		NextOffset: result.NextOffset,
		EOF:        result.EOF,
	}, nil
}

func (c *mkringClient) ExecTTYPrepare(ctx context.Context, req ExecTTYRequest) (*ExecTTYPrepareResult, error) {
	result, err := c.service.ExecTTYPrepare(ctx, c.peerKernelID, mkringcontrol.ExecTTYPreparePayload{
		KernelID:    c.kernelID,
		ContainerID: req.ContainerID,
		Command:     append([]string(nil), req.Command...),
		TTY:         req.TTY,
		Stdin:       req.Stdin,
		Stdout:      req.Stdout,
		Stderr:      req.Stderr,
	})
	if err != nil {
		return nil, err
	}
	return &ExecTTYPrepareResult{SessionID: result.SessionID}, nil
}

func (c *mkringClient) ExecTTYStart(ctx context.Context, req ExecTTYStartRequest) error {
	return c.service.ExecTTYStart(ctx, c.peerKernelID, mkringcontrol.ExecTTYStartPayload{
		KernelID:  c.kernelID,
		SessionID: req.SessionID,
	})
}

func (c *mkringClient) ExecTTYResize(ctx context.Context, req ExecTTYResizeRequest) error {
	return c.service.ExecTTYResize(ctx, c.peerKernelID, mkringcontrol.ExecTTYResizePayload{
		KernelID:  c.kernelID,
		SessionID: req.SessionID,
		Width:     req.Width,
		Height:    req.Height,
	})
}

func (c *mkringClient) ExecTTYClose(ctx context.Context, req ExecTTYCloseRequest) error {
	return c.service.ExecTTYClose(ctx, c.peerKernelID, mkringcontrol.ExecTTYClosePayload{
		KernelID:  c.kernelID,
		SessionID: req.SessionID,
	})
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
