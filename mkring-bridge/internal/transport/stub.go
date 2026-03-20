package transport

import (
	"context"
	"fmt"
	"time"

	"mkring-bridge/internal/protocol"
)

type StubTransport struct{}

func NewStubTransport() *StubTransport {
	return &StubTransport{}
}

func (t *StubTransport) WaitReady(_ context.Context, _ uint16, _ string, _ time.Duration) error {
	return nil
}

func (t *StubTransport) RoundTrip(_ context.Context, _ uint16, req protocol.Envelope) (protocol.Envelope, error) {
	switch req.Operation {
	case protocol.OpCreateContainer:
		return protocol.NewResponse(req, protocol.CreateContainerResult{
			ContainerID: "stub-" + req.ID,
			ImageRef:    "stub://" + req.KernelID,
		})
	case protocol.OpStartContainer:
		return protocol.NewResponse(req, struct{}{})
	case protocol.OpStopContainer:
		return protocol.NewResponse(req, protocol.StopContainerResult{ExitCode: 0})
	case protocol.OpRemoveContainer:
		return protocol.NewResponse(req, struct{}{})
	default:
		return protocol.Envelope{}, fmt.Errorf("unsupported op: %s", req.Operation)
	}
}
