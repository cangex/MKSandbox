package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"mk-container/pkg/agent"
	"mk-container/pkg/config"
	"mk-container/pkg/cri"
	"mk-container/pkg/kernel"
	mkrt "mk-container/pkg/runtime"
	"mk-container/pkg/streaming"
	"mk-container/pkg/util"
)

func unixSocketPath(endpoint string) (string, error) {
	if strings.HasPrefix(endpoint, "unix://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return "", err
		}
		return u.Path, nil
	}
	if strings.HasPrefix(endpoint, "/") {
		return endpoint, nil
	}
	return "", fmt.Errorf("unsupported endpoint: %s", endpoint)
}

func ensureSocketDir(path string) error {
	dir := filepath.Dir(path)
	return os.MkdirAll(dir, 0o755)
}

func main() {
	cfg := config.FromEnv()
	socketPath, err := unixSocketPath(cfg.ListenSocket)
	if err != nil {
		log.Fatalf("invalid listen socket: %v", err)
	}

	if err := ensureSocketDir(socketPath); err != nil {
		log.Fatalf("create socket dir failed: %v", err)
	}
	_ = os.Remove(socketPath)

	allocator, err := util.NewIPAllocator(cfg.PodCIDRBase, cfg.PodCIDRMask)
	if err != nil {
		log.Fatalf("ip allocator init failed: %v", err)
	}

	kernelIDAlloc, err := util.NewIntAllocator(cfg.KernelPeerIDBase, cfg.KernelPeerIDMax)
	if err != nil {
		log.Fatalf("kernel peer id allocator init failed: %v", err)
	}

	kernelManager := kernel.NewProcessManager(
		cfg.KernelStartCommand,
		cfg.KernelStopCommand,
		cfg.KernelEndpointTemplate,
		cfg.ControlTransport,
	)

	var agentFactory agent.Factory
	switch cfg.ControlTransport {
	case "mock":
		agentFactory = agent.NewMockFactory()
	case "mkring":
		agentFactory = agent.NewMkringFactory(cfg.ControlDevicePath)
	default:
		log.Fatalf("unsupported control transport: %s", cfg.ControlTransport)
	}

	engine := mkrt.NewEngine(kernelManager, agentFactory, allocator, kernelIDAlloc)
	streamServer := streaming.NewServer(nil, cfg.StreamBaseURL)
	streamServer.SetDataPlane(streaming.NewDeviceBridge(cfg.StreamDevicePath))
	streamServer.SetRemoteCommandAdapter(streaming.NewSPDYRemoteCommandAdapter(nil))

	service := cri.NewServer(engine, cfg.RuntimeName, cfg.RuntimeVersion, streamServer)
	grpcServer := grpc.NewServer()
	runtimeapi.RegisterRuntimeServiceServer(grpcServer, service)
	runtimeapi.RegisterImageServiceServer(grpcServer, service)

	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("listen failed: %v", err)
	}

	streamHTTPServer := &http.Server{
		Addr:              cfg.StreamListenAddress,
		Handler:           streamServer,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("mkcri started, endpoint=unix://%s runtime=%s", socketPath, service.String())
	log.Printf("mkcri control transport=%s control_device=%s", cfg.ControlTransport, cfg.ControlDevicePath)
	log.Printf("mkcri exec streaming started, address=%s base_url=%s device=%s", cfg.StreamListenAddress, cfg.StreamBaseURL, cfg.StreamDevicePath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := streamHTTPServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("streaming serve failed: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = streamHTTPServer.Shutdown(context.Background())
		grpcServer.GracefulStop()
		_ = lis.Close()
		_ = os.Remove(socketPath)
	}()

	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("grpc serve failed: %v", err)
	}
}
