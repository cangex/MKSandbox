package config

import (
	"os"
	"strconv"
)

// Config stores runtime-wide settings for mkcri.
type Config struct {
	ListenSocket           string
	StreamListenAddress    string
	StreamBaseURL          string
	TransportSyscallNR     uintptr
	StreamDevicePath       string
	KernelStartCommand     string
	KernelStopCommand      string
	KernelEndpointTemplate string
	ControlTransport       string
	KernelPeerIDBase       int
	KernelPeerIDMax        int
	PodCIDRBase            string
	PodCIDRMask            int
	RuntimeName            string
	RuntimeVersion         string
}

func fromEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// FromEnv loads service config from environment variables.
func FromEnv() Config {
	controlTransport := fromEnvOrDefault("MK_CONTROL_TRANSPORT", "mkring")
	transportSyscallNR := uintptr(0)
	if v := os.Getenv("MKCRI_TRANSPORT_SYSCALL_NR"); v != "" {
		if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
			transportSyscallNR = uintptr(parsed)
		}
	}

	mask := 24
	if v := os.Getenv("MK_POD_CIDR_MASK"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 30 {
			mask = parsed
		}
	}

	kernelPeerIDBase := 1
	if v := os.Getenv("MK_KERNEL_PEER_ID_BASE"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			kernelPeerIDBase = parsed
		}
	}

	kernelPeerIDMax := 255
	if v := os.Getenv("MK_KERNEL_PEER_ID_MAX"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= kernelPeerIDBase {
			kernelPeerIDMax = parsed
		}
	}

	return Config{
		ListenSocket:           fromEnvOrDefault("MKCRI_LISTEN_SOCKET", "/tmp/mkcri.sock"),
		StreamListenAddress:    fromEnvOrDefault("MKCRI_STREAM_LISTEN_ADDRESS", "127.0.0.1:10010"),
		StreamBaseURL:          fromEnvOrDefault("MKCRI_STREAM_BASE_URL", "http://127.0.0.1:10010"),
		TransportSyscallNR:     transportSyscallNR,
		StreamDevicePath:       fromEnvOrDefault("MKCRI_STREAM_DEVICE_PATH", "/dev/mkring_stream_bridge"),
		KernelStartCommand:     os.Getenv("MK_KERNEL_START_COMMAND"),
		KernelStopCommand:      os.Getenv("MK_KERNEL_STOP_COMMAND"),
		KernelEndpointTemplate: fromEnvOrDefault("MK_KERNEL_ENDPOINT_TEMPLATE", "unix:///run/mk-kernel/%s/containerd.sock"),
		ControlTransport:       controlTransport,
		KernelPeerIDBase:       kernelPeerIDBase,
		KernelPeerIDMax:        kernelPeerIDMax,
		PodCIDRBase:            fromEnvOrDefault("MK_POD_CIDR_BASE", "10.240.0.0"),
		PodCIDRMask:            mask,
		RuntimeName:            fromEnvOrDefault("MK_RUNTIME_NAME", "mkcri"),
		RuntimeVersion:         fromEnvOrDefault("MK_RUNTIME_VERSION", "0.1.0"),
	}
}
