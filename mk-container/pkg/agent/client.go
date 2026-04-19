package agent

import (
	"context"
	"errors"
	"time"
)

var ErrNotImplemented = errors.New("not implemented")

type ContainerSpec struct {
	PodID       string
	Name        string
	Image       string
	Command     []string
	Args        []string
	Env         []EnvVar
	Labels      map[string]string
	Annotations map[string]string
	LogPath     string
}

type EnvVar struct {
	Key   string
	Value string
}

type NetworkEndpoint struct {
	IP           string
	PeerKernelID uint16
}

type NetworkSpec struct {
	PodID     string
	PodIP     string
	PodCIDR   string
	Mode      string
	Endpoints []NetworkEndpoint
	Ports     []uint16
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

type ExecTTYRequest struct {
	ContainerID string
	Command     []string
	TTY         bool
	Stdin       bool
	Stdout      bool
	Stderr      bool
}

type ExecTTYPrepareResult struct {
	SessionID string
}

type ExecTTYStartRequest struct {
	SessionID string
}

type ExecTTYResizeRequest struct {
	SessionID string
	Width     uint32
	Height    uint32
}

type ExecTTYCloseRequest struct {
	SessionID string
}

type Client interface {
	WaitReady(ctx context.Context) error
	ConfigureNetwork(ctx context.Context, spec NetworkSpec) error
	ConfigureContainerEnv(ctx context.Context, podID, name string, env []EnvVar) error
	CreateContainer(ctx context.Context, spec ContainerSpec) (string, string, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string, timeout time.Duration) (int32, error)
	RemoveContainer(ctx context.Context, containerID string) error
	ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error)
	ReadLog(ctx context.Context, containerID string, offset uint64, maxBytes int) (*LogChunk, error)
	ExecTTYPrepare(ctx context.Context, req ExecTTYRequest) (*ExecTTYPrepareResult, error)
	ExecTTYStart(ctx context.Context, req ExecTTYStartRequest) error
	ExecTTYResize(ctx context.Context, req ExecTTYResizeRequest) error
	ExecTTYClose(ctx context.Context, req ExecTTYCloseRequest) error
}

type Factory interface {
	ForKernel(kernelID, endpoint string) Client
}

// SnapshotPeerReadyEnsurer is an optional host-side extension used by the
// host mkring control path. Snapshot-based kernel wake currently restores
// guest execution before the host control link is fully marked ready, so
// CreateContainer can ask the transport to mark the peer ready proactively.
type SnapshotPeerReadyEnsurer interface {
	ForcePeerReady(ctx context.Context) error
}
