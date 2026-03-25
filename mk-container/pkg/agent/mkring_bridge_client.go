package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MkringBridgeFactory sends per-kernel runtime requests to a host-side bridge
// process, which is responsible for translating them onto mkring and then into
// the target sub-kernel runtime agent.
type MkringBridgeFactory struct {
	mu           sync.Mutex
	bridgeSocket string
	baseURL      string
	httpClient   *http.Client
	clients      map[string]Client
}

type mkringBridgeClient struct {
	httpClient   *http.Client
	bridgeSocket string
	baseURL      string
	kernelID     string
	peerKernelID uint16
}

type errorClient struct {
	err error
}

type bridgeCreateContainerRequest struct {
	KernelID    string            `json:"kernel_id"`
	PodID       string            `json:"pod_id"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Command     []string          `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	LogPath     string            `json:"log_path,omitempty"`
}

type bridgeCreateContainerResponse struct {
	ContainerID string `json:"container_id"`
	ImageRef    string `json:"image_ref,omitempty"`
}

type bridgeWaitReadyRequest struct {
	KernelID      string `json:"kernel_id"`
	TimeoutMillis int64  `json:"timeout_millis,omitempty"`
}

type bridgeContainerControlRequest struct {
	KernelID      string `json:"kernel_id"`
	TimeoutMillis int64  `json:"timeout_millis,omitempty"`
}

type bridgeStopContainerResponse struct {
	ExitCode int32 `json:"exit_code"`
}

type bridgeContainerStatusResponse struct {
	State              string `json:"state"`
	ExitCode           int32  `json:"exit_code"`
	PID                int32  `json:"pid"`
	StartedAtUnixNano  uint64 `json:"started_at_unix_nano"`
	FinishedAtUnixNano uint64 `json:"finished_at_unix_nano"`
	Message            string `json:"message,omitempty"`
}

type bridgeReadLogRequest struct {
	KernelID string `json:"kernel_id"`
	Offset   uint64 `json:"offset"`
	MaxBytes uint32 `json:"max_bytes,omitempty"`
}

type bridgeReadLogResponse struct {
	NextOffset uint64 `json:"next_offset"`
	EOF        bool   `json:"eof"`
	Data       []byte `json:"data,omitempty"`
}

type bridgeExecTTYPrepareRequest struct {
	KernelID string   `json:"kernel_id"`
	Command  []string `json:"command"`
	TTY      bool     `json:"tty"`
	Stdin    bool     `json:"stdin"`
	Stdout   bool     `json:"stdout"`
	Stderr   bool     `json:"stderr"`
}

type bridgeExecTTYPrepareResponse struct {
	SessionID string `json:"session_id"`
}

type bridgeExecTTYSessionRequest struct {
	KernelID string `json:"kernel_id"`
}

type bridgeExecTTYResizeRequest struct {
	KernelID string `json:"kernel_id"`
	Width    uint32 `json:"width"`
	Height   uint32 `json:"height"`
}

type bridgeErrorResponse struct {
	Error string `json:"error"`
}

func NewMkringBridgeFactory(bridgeSocket string) *MkringBridgeFactory {
	return &MkringBridgeFactory{
		bridgeSocket: bridgeSocket,
		baseURL:      "http://mkring",
		httpClient:   newUnixSocketHTTPClient(bridgeSocket),
		clients:      map[string]Client{},
	}
}

func newUnixSocketHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}

	return &http.Client{Transport: transport}
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

func (f *MkringBridgeFactory) ForKernel(kernelID, endpoint string) Client {
	f.mu.Lock()
	defer f.mu.Unlock()

	cacheKey := kernelID + "|" + endpoint
	if c, ok := f.clients[cacheKey]; ok {
		return c
	}

	peerKernelID, err := parseMkringEndpoint(endpoint)
	if err != nil {
		c := errorClient{err: err}
		f.clients[cacheKey] = c
		return c
	}

	c := &mkringBridgeClient{
		httpClient:   f.httpClient,
		bridgeSocket: f.bridgeSocket,
		baseURL:      f.baseURL,
		kernelID:     kernelID,
		peerKernelID: peerKernelID,
	}
	f.clients[cacheKey] = c
	return c
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

func (c *mkringBridgeClient) basePath() string {
	return fmt.Sprintf("/v1/kernels/%d", c.peerKernelID)
}

func timeoutMillisFromContext(ctx context.Context) int64 {
	if deadline, ok := ctx.Deadline(); ok {
		timeout := time.Until(deadline).Milliseconds()
		if timeout < 0 {
			return 0
		}
		return timeout
	}
	return 0
}

func (c *mkringBridgeClient) doJSON(ctx context.Context, method, path string, reqBody interface{}, respBody interface{}) error {
	var body io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("mkring bridge %s request failed (socket=%s): %w", path, c.bridgeSocket, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusMultipleChoices {
		var bridgeErr bridgeErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&bridgeErr); err == nil && bridgeErr.Error != "" {
			return fmt.Errorf("mkring bridge %s: %s", path, bridgeErr.Error)
		}

		payload, _ := io.ReadAll(resp.Body)
		if len(payload) == 0 {
			return fmt.Errorf("mkring bridge %s returned status %s", path, resp.Status)
		}
		return fmt.Errorf("mkring bridge %s returned status %s: %s", path, resp.Status, strings.TrimSpace(string(payload)))
	}

	if respBody == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(respBody); err != nil {
		return fmt.Errorf("decode mkring bridge %s response: %w", path, err)
	}
	return nil
}

func (c *mkringBridgeClient) WaitReady(ctx context.Context) error {
	req := bridgeWaitReadyRequest{
		KernelID:      c.kernelID,
		TimeoutMillis: timeoutMillisFromContext(ctx),
	}
	return c.doJSON(ctx, http.MethodPost, c.basePath()+"/wait-ready", req, nil)
}

func (c *mkringBridgeClient) CreateContainer(ctx context.Context, spec ContainerSpec) (string, string, error) {
	req := bridgeCreateContainerRequest{
		KernelID:    c.kernelID,
		PodID:       spec.PodID,
		Name:        spec.Name,
		Image:       spec.Image,
		Command:     spec.Command,
		Args:        spec.Args,
		Labels:      spec.Labels,
		Annotations: spec.Annotations,
		LogPath:     spec.LogPath,
	}

	var resp bridgeCreateContainerResponse
	if err := c.doJSON(ctx, http.MethodPost, c.basePath()+"/containers", req, &resp); err != nil {
		return "", "", err
	}

	return resp.ContainerID, resp.ImageRef, nil
}

func (c *mkringBridgeClient) StartContainer(ctx context.Context, containerID string) error {
	req := bridgeContainerControlRequest{KernelID: c.kernelID}
	return c.doJSON(ctx, http.MethodPost, c.basePath()+"/containers/"+containerID+"/start", req, nil)
}

func (c *mkringBridgeClient) StopContainer(ctx context.Context, containerID string, timeout time.Duration) (int32, error) {
	req := bridgeContainerControlRequest{
		KernelID:      c.kernelID,
		TimeoutMillis: timeout.Milliseconds(),
	}

	var resp bridgeStopContainerResponse
	if err := c.doJSON(ctx, http.MethodPost, c.basePath()+"/containers/"+containerID+"/stop", req, &resp); err != nil {
		return 0, err
	}

	return resp.ExitCode, nil
}

func (c *mkringBridgeClient) RemoveContainer(ctx context.Context, containerID string) error {
	req := bridgeContainerControlRequest{KernelID: c.kernelID}
	return c.doJSON(ctx, http.MethodPost, c.basePath()+"/containers/"+containerID+"/remove", req, nil)
}

func (c *mkringBridgeClient) ContainerStatus(ctx context.Context, containerID string) (*ContainerStatus, error) {
	req := bridgeContainerControlRequest{KernelID: c.kernelID}

	var resp bridgeContainerStatusResponse
	if err := c.doJSON(ctx, http.MethodPost, c.basePath()+"/containers/"+containerID+"/status", req, &resp); err != nil {
		return nil, err
	}

	status := &ContainerStatus{
		State:    resp.State,
		ExitCode: resp.ExitCode,
		PID:      resp.PID,
		Message:  resp.Message,
	}
	if resp.StartedAtUnixNano != 0 {
		status.StartedAt = time.Unix(0, int64(resp.StartedAtUnixNano)).UTC()
	}
	if resp.FinishedAtUnixNano != 0 {
		status.FinishedAt = time.Unix(0, int64(resp.FinishedAtUnixNano)).UTC()
	}
	return status, nil
}

func (c *mkringBridgeClient) ReadLog(ctx context.Context, containerID string, offset uint64, maxBytes int) (*LogChunk, error) {
	req := bridgeReadLogRequest{
		KernelID: c.kernelID,
		Offset:   offset,
	}
	if maxBytes > 0 {
		req.MaxBytes = uint32(maxBytes)
	}

	var resp bridgeReadLogResponse
	if err := c.doJSON(ctx, http.MethodPost, c.basePath()+"/containers/"+url.PathEscape(containerID)+"/read-log", req, &resp); err != nil {
		return nil, err
	}

	return &LogChunk{
		Data:       resp.Data,
		NextOffset: resp.NextOffset,
		EOF:        resp.EOF,
	}, nil
}

func (c *mkringBridgeClient) ExecTTYPrepare(ctx context.Context, req ExecTTYRequest) (*ExecTTYPrepareResult, error) {
	body := bridgeExecTTYPrepareRequest{
		KernelID: c.kernelID,
		Command:  append([]string(nil), req.Command...),
		TTY:      req.TTY,
		Stdin:    req.Stdin,
		Stdout:   req.Stdout,
		Stderr:   req.Stderr,
	}
	var resp bridgeExecTTYPrepareResponse
	path := c.basePath() + "/containers/" + url.PathEscape(req.ContainerID) + "/exec-tty"
	if err := c.doJSON(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, err
	}
	return &ExecTTYPrepareResult{SessionID: resp.SessionID}, nil
}

func (c *mkringBridgeClient) ExecTTYStart(ctx context.Context, req ExecTTYStartRequest) error {
	path := c.basePath() + "/sessions/" + url.PathEscape(req.SessionID) + "/start"
	return c.doJSON(ctx, http.MethodPost, path, bridgeExecTTYSessionRequest{KernelID: c.kernelID}, nil)
}

func (c *mkringBridgeClient) ExecTTYResize(ctx context.Context, req ExecTTYResizeRequest) error {
	path := c.basePath() + "/sessions/" + url.PathEscape(req.SessionID) + "/resize"
	return c.doJSON(ctx, http.MethodPost, path, bridgeExecTTYResizeRequest{
		KernelID: c.kernelID,
		Width:    req.Width,
		Height:   req.Height,
	}, nil)
}

func (c *mkringBridgeClient) ExecTTYClose(ctx context.Context, req ExecTTYCloseRequest) error {
	path := c.basePath() + "/sessions/" + url.PathEscape(req.SessionID) + "/close"
	return c.doJSON(ctx, http.MethodPost, path, bridgeExecTTYSessionRequest{KernelID: c.kernelID}, nil)
}
