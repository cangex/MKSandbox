package agent

import (
	"context"
	"time"
)

type ContainerSpec struct {
	PodID       string
	Name        string
	Image       string
	Command     []string
	Args        []string
	Labels      map[string]string
	Annotations map[string]string
	LogPath     string
}

type ContainerStatus struct {
	State      string
	ExitCode   int32
	PID        int32
	StartedAt  time.Time
	FinishedAt time.Time
	Message    string
}

type LogChunk struct {
	Data       []byte
	NextOffset uint64
	EOF        bool
}

type Client interface {
	WaitReady(ctx context.Context) error
	CreateContainer(ctx context.Context, spec ContainerSpec) (string, string, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string, timeout time.Duration) (int32, error)
	RemoveContainer(ctx context.Context, containerID string) error
	ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error)
	ReadLog(ctx context.Context, containerID string, offset uint64, maxBytes int) (*LogChunk, error)
}

type Factory interface {
	ForKernel(kernelID, endpoint string) Client
}
