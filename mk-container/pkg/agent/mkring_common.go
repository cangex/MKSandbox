package agent

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type errorClient struct {
	err error
}

func parseMkringEndpoint(endpoint string) (uint16, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return 0, err
	}
	if u.Scheme != "mkring" {
		return 0, fmt.Errorf("unsupported endpoint scheme %q", u.Scheme)
	}

	rawPeerID := u.Host
	if rawPeerID == "" {
		rawPeerID = strings.TrimPrefix(u.Path, "/")
	}
	if rawPeerID == "" {
		return 0, fmt.Errorf("mkring endpoint %q missing peer id", endpoint)
	}

	peerID, err := strconv.ParseUint(rawPeerID, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("parse mkring peer id %q: %w", rawPeerID, err)
	}

	return uint16(peerID), nil
}

func (c errorClient) CreateContainer(_ context.Context, _ ContainerSpec) (string, string, error) {
	return "", "", c.err
}

func (c errorClient) WaitReady(_ context.Context) error {
	return c.err
}

func (c errorClient) StartContainer(_ context.Context, _ string) error {
	return c.err
}

func (c errorClient) StopContainer(_ context.Context, _ string, _ time.Duration) (int32, error) {
	return 0, c.err
}

func (c errorClient) RemoveContainer(_ context.Context, _ string) error {
	return c.err
}

func (c errorClient) ContainerStatus(_ context.Context, _ string) (*ContainerStatus, error) {
	return nil, c.err
}

func (c errorClient) ReadLog(_ context.Context, _ string, _ uint64, _ int) (*LogChunk, error) {
	return nil, c.err
}

func (c errorClient) ExecTTYPrepare(_ context.Context, _ ExecTTYRequest) (*ExecTTYPrepareResult, error) {
	return nil, c.err
}

func (c errorClient) ExecTTYStart(_ context.Context, _ ExecTTYStartRequest) error {
	return c.err
}

func (c errorClient) ExecTTYResize(_ context.Context, _ ExecTTYResizeRequest) error {
	return c.err
}

func (c errorClient) ExecTTYClose(_ context.Context, _ ExecTTYCloseRequest) error {
	return c.err
}
