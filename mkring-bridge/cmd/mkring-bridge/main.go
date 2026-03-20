package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"mkring-bridge/internal/config"
	"mkring-bridge/internal/httpapi"
	"mkring-bridge/internal/service"
	"mkring-bridge/internal/transport"
)

func main() {
	cfg := config.FromEnv()

	var bridgeTransport transport.Transport
	switch cfg.TransportDriver {
	case "device":
		bridgeTransport = transport.NewDeviceTransport(cfg.DevicePath)
	case "stub":
		bridgeTransport = transport.NewStubTransport()
	default:
		log.Fatalf("unsupported transport driver: %s", cfg.TransportDriver)
	}

	svc := service.New(bridgeTransport)
	server, err := httpapi.NewServer(cfg, svc)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && err != http.ErrServerClosed {
			log.Printf("shutdown error: %v", err)
		}
	}()

	log.Printf("mkring-bridge listening on %s", cfg.ListenSocket)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
