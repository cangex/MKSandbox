package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

const Version = 1

type MessageKind string

const (
	MessageKindRequest  MessageKind = "request"
	MessageKindResponse MessageKind = "response"
)

type Operation string

const (
	OpCreateContainer Operation = "create_container"
	OpStartContainer  Operation = "start_container"
	OpStopContainer   Operation = "stop_container"
	OpRemoveContainer Operation = "remove_container"
	OpStatusContainer Operation = "status_container"
	OpReadLog         Operation = "read_log"
	OpExecTTYPrepare  Operation = "exec_tty_prepare"
	OpExecTTYStart    Operation = "exec_tty_start"
	OpExecTTYResize   Operation = "exec_tty_resize"
	OpExecTTYClose    Operation = "exec_tty_close"
)

// Envelope is the logical request/response unit exchanged between the host
// bridge and the sub-kernel guest agent.
type Envelope struct {
	Version      int             `json:"version"`
	ID           string          `json:"id"`
	Kind         MessageKind     `json:"kind"`
	Operation    Operation       `json:"operation"`
	PeerKernelID uint16          `json:"peer_kernel_id"`
	KernelID     string          `json:"kernel_id"`
	SentAt       time.Time       `json:"sent_at"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	Error        *ErrorBody      `json:"error,omitempty"`
}

// Frame is the chunk-level format intended for transport over mkring's fixed
// message slots. Implementations may serialize Frame as JSON or another compact
// binary representation, but the logical fields should be preserved.
type Frame struct {
	Version   int    `json:"version"`
	MessageID string `json:"message_id"`
	Sequence  uint32 `json:"sequence"`
	Final     bool   `json:"final"`
	Payload   []byte `json:"payload"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type CreateContainerPayload struct {
	KernelID    string            `json:"kernel_id"`
	PodID       string            `json:"pod_id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Command     []string          `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	LogPath     string            `json:"log_path,omitempty"`
}

type CreateContainerResult struct {
	ContainerID string `json:"container_id"`
	ImageRef    string `json:"image_ref,omitempty"`
}

type ContainerControlPayload struct {
	KernelID      string `json:"kernel_id"`
	ContainerID   string `json:"container_id"`
	TimeoutMillis int64  `json:"timeout_millis,omitempty"`
}

type StopContainerResult struct {
	ExitCode int32 `json:"exit_code"`
}

type ContainerStatusState string

const (
	ContainerStatusUnknown ContainerStatusState = "UNKNOWN"
	ContainerStatusCreated ContainerStatusState = "CREATED"
	ContainerStatusRunning ContainerStatusState = "RUNNING"
	ContainerStatusExited  ContainerStatusState = "EXITED"
)

type ContainerStatusResult struct {
	State              ContainerStatusState `json:"state"`
	ExitCode           int32                `json:"exit_code"`
	PID                int32                `json:"pid"`
	StartedAtUnixNano  uint64               `json:"started_at_unix_nano"`
	FinishedAtUnixNano uint64               `json:"finished_at_unix_nano"`
	Message            string               `json:"message,omitempty"`
}

type ReadLogPayload struct {
	KernelID    string `json:"kernel_id"`
	ContainerID string `json:"container_id"`
	Offset      uint64 `json:"offset"`
	MaxBytes    uint32 `json:"max_bytes,omitempty"`
}

type ReadLogResult struct {
	NextOffset uint64 `json:"next_offset"`
	EOF        bool   `json:"eof"`
	Data       []byte `json:"data,omitempty"`
}

type ExecTTYPreparePayload struct {
	KernelID    string   `json:"kernel_id"`
	ContainerID string   `json:"container_id"`
	Command     []string `json:"command"`
	TTY         bool     `json:"tty"`
	Stdin       bool     `json:"stdin"`
	Stdout      bool     `json:"stdout"`
	Stderr      bool     `json:"stderr"`
}

type ExecTTYPrepareResult struct {
	SessionID string `json:"session_id"`
}

type ExecTTYStartPayload struct {
	KernelID  string `json:"kernel_id"`
	SessionID string `json:"session_id"`
}

type ExecTTYResizePayload struct {
	KernelID  string `json:"kernel_id"`
	SessionID string `json:"session_id"`
	Width     uint32 `json:"width"`
	Height    uint32 `json:"height"`
}

type ExecTTYClosePayload struct {
	KernelID  string `json:"kernel_id"`
	SessionID string `json:"session_id"`
}

func NewRequest(id string, peerKernelID uint16, kernelID string, op Operation, payload interface{}) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}

	return Envelope{
		Version:      Version,
		ID:           id,
		Kind:         MessageKindRequest,
		Operation:    op,
		PeerKernelID: peerKernelID,
		KernelID:     kernelID,
		SentAt:       time.Now().UTC(),
		Payload:      raw,
	}, nil
}

func NewResponse(req Envelope, payload interface{}) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}

	return Envelope{
		Version:      Version,
		ID:           req.ID,
		Kind:         MessageKindResponse,
		Operation:    req.Operation,
		PeerKernelID: req.PeerKernelID,
		KernelID:     req.KernelID,
		SentAt:       time.Now().UTC(),
		Payload:      raw,
	}, nil
}

func NewErrorResponse(req Envelope, code, message string) Envelope {
	return Envelope{
		Version:      Version,
		ID:           req.ID,
		Kind:         MessageKindResponse,
		Operation:    req.Operation,
		PeerKernelID: req.PeerKernelID,
		KernelID:     req.KernelID,
		SentAt:       time.Now().UTC(),
		Error: &ErrorBody{
			Code:    code,
			Message: message,
		},
	}
}

func DecodePayload(env Envelope, out interface{}) error {
	if len(env.Payload) == 0 {
		return fmt.Errorf("empty payload")
	}
	if err := json.Unmarshal(env.Payload, out); err != nil {
		return err
	}
	return nil
}

func SplitEnvelope(env Envelope, maxFramePayload int) ([]Frame, error) {
	if maxFramePayload <= 0 {
		return nil, fmt.Errorf("invalid max frame payload: %d", maxFramePayload)
	}

	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty envelope")
	}

	frames := make([]Frame, 0, (len(raw)+maxFramePayload-1)/maxFramePayload)
	for seq, start := uint32(0), 0; start < len(raw); seq, start = seq+1, start+maxFramePayload {
		end := start + maxFramePayload
		if end > len(raw) {
			end = len(raw)
		}
		frames = append(frames, Frame{
			Version:   Version,
			MessageID: env.ID,
			Sequence:  seq,
			Final:     end == len(raw),
			Payload:   raw[start:end],
		})
	}

	return frames, nil
}

func ReassembleFrames(frames []Frame) (Envelope, error) {
	if len(frames) == 0 {
		return Envelope{}, fmt.Errorf("no frames")
	}

	raw := make([]byte, 0)
	for i, frame := range frames {
		if frame.Version != Version {
			return Envelope{}, fmt.Errorf("unsupported frame version: %d", frame.Version)
		}
		if uint32(i) != frame.Sequence {
			return Envelope{}, fmt.Errorf("unexpected frame sequence: got=%d want=%d", frame.Sequence, i)
		}
		raw = append(raw, frame.Payload...)
		if frame.Final && i != len(frames)-1 {
			return Envelope{}, fmt.Errorf("final frame before end of slice")
		}
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return Envelope{}, err
	}
	return env, nil
}
