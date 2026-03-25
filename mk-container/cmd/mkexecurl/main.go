package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func dialUnixSocket(ctx context.Context, endpoint string) (*grpc.ClientConn, error) {
	socketPath := endpoint
	socketPath = strings.TrimPrefix(socketPath, "unix://")

	return grpc.DialContext(
		ctx,
		"unix://"+socketPath,
		grpc.WithInsecure(),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		}),
	)
}

func main() {
	socket := flag.String("socket", "/tmp/mkcri.sock", "CRI runtime unix socket path")
	containerID := flag.String("container", "", "target container ID")
	tty := flag.Bool("tty", true, "request a TTY exec session")
	stdin := flag.Bool("stdin", true, "attach stdin")
	stdout := flag.Bool("stdout", true, "attach stdout")
	stderr := flag.Bool("stderr", false, "attach stderr")
	timeout := flag.Duration("timeout", 5*time.Second, "dial/request timeout")
	flag.Parse()

	if *containerID == "" {
		log.Fatal("-container is required")
	}
	if flag.NArg() == 0 {
		log.Fatal("exec command is required, e.g. mkexecurl -container <id> -- sh")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	conn, err := dialUnixSocket(ctx, *socket)
	if err != nil {
		log.Fatalf("dial mkcri failed: %v", err)
	}
	defer conn.Close()

	client := runtimeapi.NewRuntimeServiceClient(conn)
	resp, err := client.Exec(ctx, &runtimeapi.ExecRequest{
		ContainerId: *containerID,
		Cmd:         flag.Args(),
		Tty:         *tty,
		Stdin:       *stdin,
		Stdout:      *stdout,
		Stderr:      *stderr,
	})
	if err != nil {
		log.Fatalf("Exec RPC failed: %v", err)
	}

	fmt.Println(resp.GetUrl())
}
