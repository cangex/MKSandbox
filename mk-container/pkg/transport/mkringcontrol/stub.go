package mkringcontrol

import (
	"context"
	"fmt"
	"time"
)

type StubTransport struct{}

func NewStubTransport() *StubTransport {
	return &StubTransport{}
}

func (t *StubTransport) WaitReady(_ context.Context, _ uint16, _ string, _ time.Duration) error {
	return nil
}

func (t *StubTransport) ForcePeerReady(_ context.Context, _ uint16, _ string) error {
	return nil
}

func (t *StubTransport) RoundTrip(_ context.Context, _ uint16, req Envelope) (Envelope, error) {
	switch req.Operation {
	case OpCreateContainer:
		return NewResponse(req, CreateContainerResult{
			ContainerID: "stub-" + req.ID,
			ImageRef:    "stub://" + req.KernelID,
		})
	case OpStartContainer:
		return NewResponse(req, struct{}{})
	case OpStopContainer:
		return NewResponse(req, StopContainerResult{ExitCode: 0})
	case OpRemoveContainer:
		return NewResponse(req, struct{}{})
	case OpExecTTYPrepare:
		return NewResponse(req, ExecTTYPrepareResult{
			SessionID: "stub-exec-" + req.ID,
		})
	case OpExecTTYStart:
		return NewResponse(req, struct{}{})
	case OpExecTTYResize:
		return NewResponse(req, struct{}{})
	case OpExecTTYClose:
		return NewResponse(req, struct{}{})
	default:
		return Envelope{}, fmt.Errorf("unsupported op: %s", req.Operation)
	}
}
