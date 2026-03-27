package kernel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// ProcessManager calls external commands to control sub-kernels.
// Commands are optional; when empty, it operates in simulation mode.
type ProcessManager struct {
	startCommand      string
	stopCommand       string
	endpointTemplate  string
	controlTransport  string
	startedKernels    map[string]startedKernel
	startedKernelsMux sync.Mutex
}

type startedKernel struct {
	peerKernelID uint16
}

func NewProcessManager(startCmd, stopCmd, endpointTemplate, controlTransport string) *ProcessManager {
	return &ProcessManager{
		startCommand:     strings.TrimSpace(startCmd),
		stopCommand:      strings.TrimSpace(stopCmd),
		endpointTemplate: endpointTemplate,
		controlTransport: strings.TrimSpace(controlTransport),
		startedKernels:   map[string]startedKernel{},
	}
}

func splitCommand(command string) (string, []string, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "", nil, fmt.Errorf("empty command")
	}
	return parts[0], parts[1:], nil
}

func normalizeBootMode(mode BootMode) BootMode {
	switch mode {
	case BootModeSnapshot:
		return BootModeSnapshot
	case BootModeColdBoot, "":
		return BootModeColdBoot
	default:
		return BootModeColdBoot
	}
}

func (m *ProcessManager) runKernelCommand(ctx context.Context, command, kernelID string, peerKernelID uint16, bootMode BootMode) error {
	bin, args, err := splitCommand(command)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	bootMode = normalizeBootMode(bootMode)
	cmd.Env = append(os.Environ(),
		"MK_KERNEL_ID="+kernelID,
		"MK_KERNEL_PEER_ID="+strconv.FormatUint(uint64(peerKernelID), 10),
		"MK_KERNEL_BOOT_MODE="+string(bootMode),
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kernel command failed: %w, output=%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *ProcessManager) kernelEndpoint(kernelID string, peerKernelID uint16) string {
	switch strings.ToLower(strings.TrimSpace(m.controlTransport)) {
	case "mkring":
		return fmt.Sprintf("mkring://%d?kernel_id=%s", peerKernelID, kernelID)
	}
	return fmt.Sprintf(m.endpointTemplate, kernelID)
}

func (m *ProcessManager) StartKernel(ctx context.Context, req StartRequest) (KernelInstance, error) {
	bootMode := normalizeBootMode(req.BootMode)
	if m.startCommand != "" {
		if err := m.runKernelCommand(ctx, m.startCommand, req.KernelID, req.PeerKernelID, bootMode); err != nil {
			return KernelInstance{}, err
		}
	}

	m.startedKernelsMux.Lock()
	m.startedKernels[req.KernelID] = startedKernel{peerKernelID: req.PeerKernelID}
	m.startedKernelsMux.Unlock()

	return KernelInstance{
		KernelID:      req.KernelID,
		PeerKernelID:  req.PeerKernelID,
		Endpoint:      m.kernelEndpoint(req.KernelID, req.PeerKernelID),
		SkipWaitReady: bootMode == BootModeSnapshot,
	}, nil
}

func (m *ProcessManager) StopKernel(ctx context.Context, kernelID string) error {
	m.startedKernelsMux.Lock()
	started, exists := m.startedKernels[kernelID]
	if exists {
		delete(m.startedKernels, kernelID)
	}
	m.startedKernelsMux.Unlock()

	if !exists {
		return nil
	}

	if m.stopCommand != "" {
		if err := m.runKernelCommand(ctx, m.stopCommand, kernelID, started.peerKernelID, BootModeColdBoot); err != nil {
			return err
		}
	}

	return nil
}
