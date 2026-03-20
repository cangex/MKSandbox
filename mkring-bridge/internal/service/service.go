package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"mkring-bridge/internal/protocol"
	"mkring-bridge/internal/transport"
)

type Service struct {
	transport transport.Transport
}

func New(transport transport.Transport) *Service {
	return &Service{transport: transport}
}

func newRequestID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (s *Service) WaitReady(ctx context.Context, peerKernelID uint16, kernelID string, timeout time.Duration) error {
	return s.transport.WaitReady(ctx, peerKernelID, kernelID, timeout)
}

func (s *Service) CreateContainer(ctx context.Context, peerKernelID uint16, payload protocol.CreateContainerPayload) (protocol.CreateContainerResult, error) {
	req, err := protocol.NewRequest(newRequestID(), peerKernelID, payload.KernelID, protocol.OpCreateContainer, payload)
	if err != nil {
		return protocol.CreateContainerResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return protocol.CreateContainerResult{}, err
	}
	if resp.Error != nil {
		return protocol.CreateContainerResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result protocol.CreateContainerResult
	if err := protocol.DecodePayload(resp, &result); err != nil {
		return protocol.CreateContainerResult{}, err
	}
	return result, nil
}

func (s *Service) StartContainer(ctx context.Context, peerKernelID uint16, payload protocol.ContainerControlPayload) error {
	req, err := protocol.NewRequest(newRequestID(), peerKernelID, payload.KernelID, protocol.OpStartContainer, payload)
	if err != nil {
		return err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return nil
}

func (s *Service) StopContainer(ctx context.Context, peerKernelID uint16, payload protocol.ContainerControlPayload) (protocol.StopContainerResult, error) {
	req, err := protocol.NewRequest(newRequestID(), peerKernelID, payload.KernelID, protocol.OpStopContainer, payload)
	if err != nil {
		return protocol.StopContainerResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return protocol.StopContainerResult{}, err
	}
	if resp.Error != nil {
		return protocol.StopContainerResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result protocol.StopContainerResult
	if err := protocol.DecodePayload(resp, &result); err != nil {
		return protocol.StopContainerResult{}, err
	}
	return result, nil
}

func (s *Service) RemoveContainer(ctx context.Context, peerKernelID uint16, payload protocol.ContainerControlPayload) error {
	req, err := protocol.NewRequest(newRequestID(), peerKernelID, payload.KernelID, protocol.OpRemoveContainer, payload)
	if err != nil {
		return err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return nil
}

func (s *Service) ContainerStatus(ctx context.Context, peerKernelID uint16, payload protocol.ContainerControlPayload) (protocol.ContainerStatusResult, error) {
	req, err := protocol.NewRequest(newRequestID(), peerKernelID, payload.KernelID, protocol.OpStatusContainer, payload)
	if err != nil {
		return protocol.ContainerStatusResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return protocol.ContainerStatusResult{}, err
	}
	if resp.Error != nil {
		return protocol.ContainerStatusResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result protocol.ContainerStatusResult
	if err := protocol.DecodePayload(resp, &result); err != nil {
		return protocol.ContainerStatusResult{}, err
	}
	return result, nil
}

func (s *Service) ReadLog(ctx context.Context, peerKernelID uint16, payload protocol.ReadLogPayload) (protocol.ReadLogResult, error) {
	req, err := protocol.NewRequest(newRequestID(), peerKernelID, payload.KernelID, protocol.OpReadLog, payload)
	if err != nil {
		return protocol.ReadLogResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return protocol.ReadLogResult{}, err
	}
	if resp.Error != nil {
		return protocol.ReadLogResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result protocol.ReadLogResult
	if err := protocol.DecodePayload(resp, &result); err != nil {
		return protocol.ReadLogResult{}, err
	}
	return result, nil
}
