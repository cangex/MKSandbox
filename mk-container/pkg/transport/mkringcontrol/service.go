package mkringcontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type Service struct {
	transport Transport
}

func New(transport Transport) *Service {
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

func (s *Service) ForcePeerReady(ctx context.Context, peerKernelID uint16, kernelID string) error {
	return s.transport.ForcePeerReady(ctx, peerKernelID, kernelID)
}

func (s *Service) ConfigureNetwork(ctx context.Context, peerKernelID uint16, payload ConfigureNetworkPayload) error {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpConfigureNetwork, payload)
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

func (s *Service) ConfigureContainerEnv(ctx context.Context, peerKernelID uint16, payload ConfigureEnvPayload) error {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpConfigureEnv, payload)
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

func (s *Service) CreateContainer(ctx context.Context, peerKernelID uint16, payload CreateContainerPayload) (CreateContainerResult, error) {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpCreateContainer, payload)
	if err != nil {
		return CreateContainerResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return CreateContainerResult{}, err
	}
	if resp.Error != nil {
		return CreateContainerResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result CreateContainerResult
	if err := DecodePayload(resp, &result); err != nil {
		return CreateContainerResult{}, err
	}
	return result, nil
}

func (s *Service) StartContainer(ctx context.Context, peerKernelID uint16, payload ContainerControlPayload) error {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpStartContainer, payload)
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

func (s *Service) StopContainer(ctx context.Context, peerKernelID uint16, payload ContainerControlPayload) (StopContainerResult, error) {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpStopContainer, payload)
	if err != nil {
		return StopContainerResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return StopContainerResult{}, err
	}
	if resp.Error != nil {
		return StopContainerResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result StopContainerResult
	if err := DecodePayload(resp, &result); err != nil {
		return StopContainerResult{}, err
	}
	return result, nil
}

func (s *Service) RemoveContainer(ctx context.Context, peerKernelID uint16, payload ContainerControlPayload) error {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpRemoveContainer, payload)
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

func (s *Service) ContainerStatus(ctx context.Context, peerKernelID uint16, payload ContainerControlPayload) (ContainerStatusResult, error) {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpStatusContainer, payload)
	if err != nil {
		return ContainerStatusResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return ContainerStatusResult{}, err
	}
	if resp.Error != nil {
		return ContainerStatusResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result ContainerStatusResult
	if err := DecodePayload(resp, &result); err != nil {
		return ContainerStatusResult{}, err
	}
	return result, nil
}

func (s *Service) ReadLog(ctx context.Context, peerKernelID uint16, payload ReadLogPayload) (ReadLogResult, error) {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpReadLog, payload)
	if err != nil {
		return ReadLogResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return ReadLogResult{}, err
	}
	if resp.Error != nil {
		return ReadLogResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	var result ReadLogResult
	if err := DecodePayload(resp, &result); err != nil {
		return ReadLogResult{}, err
	}
	return result, nil
}

func (s *Service) ExecTTYPrepare(ctx context.Context, peerKernelID uint16, payload ExecTTYPreparePayload) (ExecTTYPrepareResult, error) {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpExecTTYPrepare, payload)
	if err != nil {
		return ExecTTYPrepareResult{}, err
	}

	resp, err := s.transport.RoundTrip(ctx, peerKernelID, req)
	if err != nil {
		return ExecTTYPrepareResult{}, err
	}
	if resp.Error != nil {
		return ExecTTYPrepareResult{}, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}

	var result ExecTTYPrepareResult
	if err := DecodePayload(resp, &result); err != nil {
		return ExecTTYPrepareResult{}, err
	}
	return result, nil
}

func (s *Service) ExecTTYStart(ctx context.Context, peerKernelID uint16, payload ExecTTYStartPayload) error {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpExecTTYStart, payload)
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

func (s *Service) ExecTTYResize(ctx context.Context, peerKernelID uint16, payload ExecTTYResizePayload) error {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpExecTTYResize, payload)
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

func (s *Service) ExecTTYClose(ctx context.Context, peerKernelID uint16, payload ExecTTYClosePayload) error {
	req, err := NewRequest(newRequestID(), peerKernelID, payload.KernelID, OpExecTTYClose, payload)
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
