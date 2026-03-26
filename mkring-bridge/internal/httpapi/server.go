package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mkring-bridge/internal/config"
	"mkring-bridge/internal/protocol"
	"mkring-bridge/internal/service"
)

type Server struct {
	cfg        config.Config
	service    *service.Service
	httpServer *http.Server
	listener   net.Listener
}

type errorResponse struct {
	Error string `json:"error"`
}

type waitReadyRequest struct {
	KernelID      string `json:"kernel_id"`
	TimeoutMillis int64  `json:"timeout_millis,omitempty"`
}

type forcePeerReadyRequest struct {
	KernelID string `json:"kernel_id"`
}

type readLogRequest struct {
	KernelID string `json:"kernel_id"`
	Offset   uint64 `json:"offset"`
	MaxBytes uint32 `json:"max_bytes,omitempty"`
}

func NewServer(cfg config.Config, svc *service.Service) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.ListenSocket), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(cfg.ListenSocket)

	lis, err := net.Listen("unix", cfg.ListenSocket)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:      cfg,
		service:  svc,
		listener: lis,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/kernels/", s.handleKernels)
	mux.HandleFunc("/healthz", s.handleHealthz)

	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	return s, nil
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.Serve(s.listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	defer os.Remove(s.cfg.ListenSocket)
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int, out interface{}) bool {
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}

func parsePeerKernelID(raw string) (uint16, error) {
	v, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		return 0, err
	}
	return uint16(v), nil
}

func (s *Server) handleKernels(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/kernels/")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown path: %s", r.URL.Path))
		return
	}

	peerKernelID, err := parsePeerKernelID(parts[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid peer kernel id: %w", err))
		return
	}

	switch {
	case len(parts) == 2 && parts[1] == "wait-ready" && r.Method == http.MethodPost:
		s.handleWaitReady(w, r, peerKernelID)
	case len(parts) == 2 && parts[1] == "peer-ready" && r.Method == http.MethodPost:
		s.handleForcePeerReady(w, r, peerKernelID)
	case len(parts) == 2 && parts[1] == "containers" && r.Method == http.MethodPost:
		s.handleCreateContainer(w, r, peerKernelID)
	case len(parts) == 4 && parts[1] == "containers" && parts[3] == "start" && r.Method == http.MethodPost:
		s.handleStartContainer(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "containers" && parts[3] == "stop" && r.Method == http.MethodPost:
		s.handleStopContainer(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "containers" && parts[3] == "remove" && r.Method == http.MethodPost:
		s.handleRemoveContainer(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "containers" && parts[3] == "status" && r.Method == http.MethodPost:
		s.handleContainerStatus(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "containers" && parts[3] == "read-log" && r.Method == http.MethodPost:
		s.handleReadLog(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "containers" && parts[3] == "exec-tty" && r.Method == http.MethodPost:
		s.handleExecTTYPrepare(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "sessions" && parts[3] == "start" && r.Method == http.MethodPost:
		s.handleExecTTYStart(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "sessions" && parts[3] == "resize" && r.Method == http.MethodPost:
		s.handleExecTTYResize(w, r, peerKernelID, parts[2])
	case len(parts) == 4 && parts[1] == "sessions" && parts[3] == "close" && r.Method == http.MethodPost:
		s.handleExecTTYClose(w, r, peerKernelID, parts[2])
	default:
		writeError(w, http.StatusNotFound, fmt.Errorf("unknown path: %s", r.URL.Path))
	}
}

func (s *Server) handleWaitReady(w http.ResponseWriter, r *http.Request, peerKernelID uint16) {
	var req waitReadyRequest
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}

	timeout := time.Duration(req.TimeoutMillis) * time.Millisecond
	if timeout <= 0 {
		timeout = s.cfg.DefaultTimeout
	}

	if err := s.service.WaitReady(r.Context(), peerKernelID, req.KernelID, timeout); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleForcePeerReady(w http.ResponseWriter, r *http.Request, peerKernelID uint16) {
	var req forcePeerReadyRequest
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}

	if err := s.service.ForcePeerReady(r.Context(), peerKernelID, req.KernelID); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleCreateContainer(w http.ResponseWriter, r *http.Request, peerKernelID uint16) {
	var req protocol.CreateContainerPayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}

	resp, err := s.service.CreateContainer(r.Context(), peerKernelID, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStartContainer(w http.ResponseWriter, r *http.Request, peerKernelID uint16, containerID string) {
	var req protocol.ContainerControlPayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.ContainerID, _ = url.PathUnescape(containerID)

	if err := s.service.StartContainer(r.Context(), peerKernelID, req); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStopContainer(w http.ResponseWriter, r *http.Request, peerKernelID uint16, containerID string) {
	var req protocol.ContainerControlPayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.ContainerID, _ = url.PathUnescape(containerID)

	resp, err := s.service.StopContainer(r.Context(), peerKernelID, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRemoveContainer(w http.ResponseWriter, r *http.Request, peerKernelID uint16, containerID string) {
	var req protocol.ContainerControlPayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.ContainerID, _ = url.PathUnescape(containerID)

	if err := s.service.RemoveContainer(r.Context(), peerKernelID, req); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleContainerStatus(w http.ResponseWriter, r *http.Request, peerKernelID uint16, containerID string) {
	var req protocol.ContainerControlPayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.ContainerID, _ = url.PathUnescape(containerID)

	resp, err := s.service.ContainerStatus(r.Context(), peerKernelID, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleReadLog(w http.ResponseWriter, r *http.Request, peerKernelID uint16, containerID string) {
	var req readLogRequest
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}

	payload := protocol.ReadLogPayload{
		KernelID:    req.KernelID,
		ContainerID: containerID,
		Offset:      req.Offset,
		MaxBytes:    req.MaxBytes,
	}
	payload.ContainerID, _ = url.PathUnescape(containerID)

	resp, err := s.service.ReadLog(r.Context(), peerKernelID, payload)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleExecTTYPrepare(w http.ResponseWriter, r *http.Request, peerKernelID uint16, containerID string) {
	var req protocol.ExecTTYPreparePayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.ContainerID, _ = url.PathUnescape(containerID)
	resp, err := s.service.ExecTTYPrepare(r.Context(), peerKernelID, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleExecTTYStart(w http.ResponseWriter, r *http.Request, peerKernelID uint16, sessionID string) {
	var req protocol.ExecTTYStartPayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.SessionID, _ = url.PathUnescape(sessionID)
	if err := s.service.ExecTTYStart(r.Context(), peerKernelID, req); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleExecTTYResize(w http.ResponseWriter, r *http.Request, peerKernelID uint16, sessionID string) {
	var req protocol.ExecTTYResizePayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.SessionID, _ = url.PathUnescape(sessionID)
	if err := s.service.ExecTTYResize(r.Context(), peerKernelID, req); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleExecTTYClose(w http.ResponseWriter, r *http.Request, peerKernelID uint16, sessionID string) {
	var req protocol.ExecTTYClosePayload
	if !decodeJSON(w, r, s.cfg.MessageMaxBytes, &req) {
		return
	}
	req.SessionID, _ = url.PathUnescape(sessionID)
	if err := s.service.ExecTTYClose(r.Context(), peerKernelID, req); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}
