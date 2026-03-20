package model

import "time"

type PodState string

const (
	PodStateReady    PodState = "READY"
	PodStateNotReady PodState = "NOTREADY"
)

type ContainerState string

const (
	ContainerStateCreated ContainerState = "CREATED"
	ContainerStateRunning ContainerState = "RUNNING"
	ContainerStateExited  ContainerState = "EXITED"
)

type Kernel struct {
	ID           string
	PeerKernelID uint16
	Endpoint     string
	StartedAt    time.Time
}

type Pod struct {
	ID             string
	Name           string
	Namespace      string
	UID            string
	Attempt        uint32
	CreatedAt      time.Time
	State          PodState
	Labels         map[string]string
	Annotations    map[string]string
	KernelID       string
	RuntimeHandler string
	IP             string
}

type Container struct {
	ID          string
	PodID       string
	Name        string
	Attempt     uint32
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
	State       ContainerState
	ExitCode    int32
	Reason      string
	Message     string
	Image       string
	ImageRef    string
	Labels      map[string]string
	Annotations map[string]string
	LogPath     string
}
