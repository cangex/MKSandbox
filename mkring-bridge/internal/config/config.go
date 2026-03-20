package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	ListenSocket    string
	TransportDriver string
	DevicePath      string
	MessageMaxBytes int
	ShutdownTimeout time.Duration
	DefaultTimeout  time.Duration
}

func fromEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func FromEnv() Config {
	messageMaxBytes := 64 * 1024
	if v := os.Getenv("MKRING_BRIDGE_MESSAGE_MAX_BYTES"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			messageMaxBytes = parsed
		}
	}

	shutdownTimeout := 5 * time.Second
	if v := os.Getenv("MKRING_BRIDGE_SHUTDOWN_TIMEOUT_SEC"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			shutdownTimeout = time.Duration(parsed) * time.Second
		}
	}

	defaultTimeout := 30 * time.Second
	if v := os.Getenv("MKRING_BRIDGE_DEFAULT_TIMEOUT_SEC"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			defaultTimeout = time.Duration(parsed) * time.Second
		}
	}

	return Config{
		ListenSocket:    fromEnvOrDefault("MKRING_BRIDGE_LISTEN_SOCKET", "/run/mk-container/mkring-bridge.sock"),
		TransportDriver: fromEnvOrDefault("MKRING_BRIDGE_TRANSPORT", "device"),
		DevicePath:      fromEnvOrDefault("MKRING_BRIDGE_DEVICE_PATH", "/dev/mkring_container_bridge"),
		MessageMaxBytes: messageMaxBytes,
		ShutdownTimeout: shutdownTimeout,
		DefaultTimeout:  defaultTimeout,
	}
}
