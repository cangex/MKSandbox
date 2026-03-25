package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moby/spdystream"
)

type spdyStreamSlot struct {
	once   sync.Once
	ready  chan struct{}
	mu     sync.RWMutex
	stream *spdystream.Stream
}

func newSPDYStreamSlot() *spdyStreamSlot {
	return &spdyStreamSlot{ready: make(chan struct{})}
}

func (s *spdyStreamSlot) set(stream *spdystream.Stream) {
	s.once.Do(func() {
		s.mu.Lock()
		s.stream = stream
		s.mu.Unlock()
		close(s.ready)
	})
}

func (s *spdyStreamSlot) wait(ctx context.Context) (*spdystream.Stream, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ready:
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.stream == nil {
			return nil, io.EOF
		}
		return s.stream, nil
	}
}

type spdyStreamSet struct {
	stdinSlot  *spdyStreamSlot
	stdoutSlot *spdyStreamSlot
	errorSlot  *spdyStreamSlot
	resizeSlot *spdyStreamSlot
}

func newSPDYStreamSet() *spdyStreamSet {
	return &spdyStreamSet{
		stdinSlot:  newSPDYStreamSlot(),
		stdoutSlot: newSPDYStreamSlot(),
		errorSlot:  newSPDYStreamSlot(),
		resizeSlot: newSPDYStreamSlot(),
	}
}

type remoteCommandStatus struct {
	Kind       string                      `json:"kind,omitempty"`
	APIVersion string                      `json:"apiVersion,omitempty"`
	Status     string                      `json:"status,omitempty"`
	Message    string                      `json:"message,omitempty"`
	Reason     string                      `json:"reason,omitempty"`
	Details    *remoteCommandStatusDetails `json:"details,omitempty"`
	Code       int32                       `json:"code,omitempty"`
}

type remoteCommandStatusDetails struct {
	Causes []remoteCommandStatusCause `json:"causes,omitempty"`
}

type remoteCommandStatusCause struct {
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

func (s *spdyStreamSet) handleStream(stream *spdystream.Stream) {
	if stream == nil {
		return
	}

	streamType := strings.ToLower(stream.Headers().Get(remoteCommandHeaderStreamType))
	switch streamType {
	case RemoteCommandStreamStdin, RemoteCommandStreamStdout, RemoteCommandStreamError, RemoteCommandStreamResize:
		if err := stream.SendReply(http.Header{}, false); err != nil {
			_ = stream.Reset()
			return
		}
	default:
		_ = stream.Refuse()
		return
	}

	switch streamType {
	case RemoteCommandStreamStdin:
		s.stdinSlot.set(stream)
	case RemoteCommandStreamStdout:
		s.stdoutSlot.set(stream)
	case RemoteCommandStreamError:
		s.errorSlot.set(stream)
	case RemoteCommandStreamResize:
		s.resizeSlot.set(stream)
	}
}

func remoteCommandErrorPayload(protocol, msg string) []byte {
	if protocol != RemoteCommandProtocolV4 {
		return []byte(msg)
	}
	payload, err := json.Marshal(remoteCommandStatus{
		Kind:       "Status",
		APIVersion: "v1",
		Status:     "Failure",
		Message:    strings.TrimSpace(msg),
		Reason:     "Unknown",
		Code:       500,
	})
	if err != nil {
		return []byte(msg)
	}
	return payload
}

func remoteCommandExitPayload(protocol string, exitCode int32) []byte {
	message := fmt.Sprintf("command terminated with non-zero exit code: %d", exitCode)
	if protocol != RemoteCommandProtocolV4 {
		return []byte(message)
	}
	payload, err := json.Marshal(remoteCommandStatus{
		Kind:       "Status",
		APIVersion: "v1",
		Status:     "Failure",
		Message:    message,
		Reason:     "NonZeroExitCode",
		Details: &remoteCommandStatusDetails{
			Causes: []remoteCommandStatusCause{{
				Reason:  "ExitCode",
				Message: strconv.Itoa(int(exitCode)),
			}},
		},
		Code: 500,
	})
	if err != nil {
		return []byte(message)
	}
	return payload
}

func (s *spdyStreamSet) writeError(protocol, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCommandErrorTimeout)
	defer cancel()

	stream, err := s.errorSlot.wait(ctx)
	if err != nil || stream == nil {
		return
	}
	_, _ = stream.Write(remoteCommandErrorPayload(protocol, msg))
	_ = stream.Close()
}

func (s *spdyStreamSet) writeExit(protocol string, exitCode int32) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCommandErrorTimeout)
	defer cancel()

	stream, err := s.errorSlot.wait(ctx)
	if err != nil || stream == nil {
		return
	}
	_, _ = stream.Write(remoteCommandExitPayload(protocol, exitCode))
	_ = stream.Close()
}

func (s *spdyStreamSet) pumpStdin(ctx context.Context, data DataPlaneSession) {
	if data == nil {
		return
	}

	stream, err := s.stdinSlot.wait(ctx)
	if err != nil || stream == nil {
		return
	}

	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if sendErr := data.SendStdin(ctx, buf[:n]); sendErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (s *spdyStreamSet) waitStdout(ctx context.Context) (*spdystream.Stream, error) {
	return s.stdoutSlot.wait(ctx)
}

func (s *spdyStreamSet) writeStdout(stream *spdystream.Stream, chunk []byte) error {
	if stream == nil || len(chunk) == 0 {
		return nil
	}
	_, err := stream.Write(chunk)
	return err
}

func (s *spdyStreamSet) serveStdoutUntilExit(ctx context.Context, stream *spdystream.Stream, data DataPlaneSession) (int32, bool, error) {
	if data == nil {
		return 0, false, fmt.Errorf("tty exec streaming data plane is not wired yet")
	}

	for {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case chunk, ok := <-data.Output():
			if !ok {
				return 0, false, nil
			}
			if err := s.writeStdout(stream, chunk); err != nil {
				return 0, false, err
			}
		case exitCode, ok := <-data.Exit():
			if ok {
				for {
					select {
					case chunk, outOK := <-data.Output():
						if !outOK {
							return exitCode, true, nil
						}
						if err := s.writeStdout(stream, chunk); err != nil {
							return exitCode, true, err
						}
					default:
						return exitCode, true, nil
					}
				}
			}
			return 0, false, nil
		}
	}
}

func (s *spdyStreamSet) closeStdout(ctx context.Context) {
	stream, err := s.stdoutSlot.wait(ctx)
	if err != nil || stream == nil {
		return
	}
	_ = stream.Close()
}

func (s *spdyStreamSet) pumpResize(ctx context.Context, exec ExecSession) {
	if exec == nil || exec.Session() == nil || exec.Session().ResizeFn == nil {
		return
	}

	stream, err := s.resizeSlot.wait(ctx)
	if err != nil || stream == nil {
		return
	}

	for {
		payload, readErr := stream.ReadData()
		if len(payload) > 0 {
			ev, err := parseResizeMessage(payload)
			if err == nil {
				_ = exec.Session().ResizeFn(ctx, ev)
			}
		}
		if readErr != nil {
			return
		}
	}
}

type SPDYRemoteCommandAdapter struct {
	supportedProtocols []string
}

func NewSPDYRemoteCommandAdapter(protocols []string) *SPDYRemoteCommandAdapter {
	if len(protocols) == 0 {
		protocols = DefaultRemoteCommandProtocols
	}
	cp := append([]string(nil), protocols...)
	return &SPDYRemoteCommandAdapter{supportedProtocols: cp}
}

func (a *SPDYRemoteCommandAdapter) upgradeConnection(w http.ResponseWriter, protocol string) (*spdystream.Connection, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("response writer does not support hijacking")
	}

	w.Header().Set(remoteCommandHeaderConnection, "Upgrade")
	w.Header().Set(remoteCommandHeaderUpgrade, remoteCommandUpgradeSPDY31)
	w.Header().Set(remoteCommandHeaderProtocolVersion, protocol)
	w.Header().Set("X-Remotecommand-Adapter", "spdy-v1")
	w.WriteHeader(http.StatusSwitchingProtocols)

	conn, _, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}

	spdyConn, err := spdystream.NewConnection(conn, true)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	spdyConn.SetCloseTimeout(2 * time.Second)
	return spdyConn, nil
}

func (a *SPDYRemoteCommandAdapter) ServeExec(_ context.Context, w http.ResponseWriter, r *http.Request, exec ExecSession) error {
	if !LooksLikeRemoteCommandRequest(r) {
		return ErrRemoteCommandNotImplemented
	}
	if exec == nil || exec.Session() == nil {
		return fmt.Errorf("exec session is required")
	}
	if !exec.Session().TTY {
		http.Error(w, "remotecommand exec requires tty session", http.StatusBadRequest)
		return nil
	}

	protocol := negotiateRemoteCommandProtocol(r, a.supportedProtocols)
	if protocol == "" {
		http.Error(w, "unsupported remotecommand stream protocol", http.StatusBadRequest)
		return nil
	}

	dataSession := exec.DataPlane()
	if dataSession == nil {
		http.Error(w, "tty exec streaming data plane is not wired yet", http.StatusNotImplemented)
		return nil
	}

	spdyConn, err := a.upgradeConnection(w, protocol)
	if err != nil {
		return err
	}
	streams := newSPDYStreamSet()
	go spdyConn.Serve(streams.handleStream)

	startCtx, cancelStart := context.WithTimeout(context.Background(), remoteCommandStartTimeout)
	defer cancelStart()
	if _, err := streams.stdoutSlot.wait(startCtx); err != nil {
		streams.writeError(protocol, "stdout stream was not established\n")
		_ = spdyConn.Close()
		return nil
	}

	if err := exec.Start(startCtx); err != nil {
		streams.writeError(protocol, fmt.Sprintf("exec start failed: %v\n", err))
		_ = spdyConn.Close()
		return nil
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	go streams.pumpStdin(runCtx, dataSession)
	go streams.pumpResize(runCtx, exec)
	stdoutStream, err := streams.waitStdout(runCtx)
	if err != nil {
		streams.writeError(protocol, fmt.Sprintf("stdout stream failed: %v\n", err))
		_ = spdyConn.Close()
		return nil
	}

	for {
		select {
		case <-spdyConn.CloseChan():
			cancelRun()
			return nil
		default:
			exitCode, exited, err := streams.serveStdoutUntilExit(runCtx, stdoutStream, dataSession)
			cancelRun()
			if err != nil && err != io.EOF && err != context.Canceled {
				streams.writeError(protocol, fmt.Sprintf("stdout stream failed: %v\n", err))
			}
			if exited {
				_ = exec.MarkExited(exitCode)
				if exitCode != 0 {
					streams.writeExit(protocol, exitCode)
				}
			}
			closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
			streams.closeStdout(closeCtx)
			closeCancel()
			_ = spdyConn.Close()
			return nil
		}
	}
}

func negotiateRemoteCommandProtocol(r *http.Request, supported []string) string {
	requested := requestedRemoteCommandProtocols(r)
	if len(requested) == 0 {
		if len(supported) == 0 {
			return ""
		}
		return supported[0]
	}

	for _, candidate := range requested {
		for _, supportedProtocol := range supported {
			if candidate == supportedProtocol {
				return candidate
			}
		}
	}
	return ""
}

func requestedRemoteCommandProtocols(r *http.Request) []string {
	if r == nil {
		return nil
	}
	values := r.Header.Values(remoteCommandHeaderProtocolVersion)
	if len(values) == 0 {
		if single := r.Header.Get(remoteCommandHeaderProtocolVersion); single != "" {
			values = []string{single}
		}
	}

	var protocols []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			protocols = append(protocols, part)
		}
	}
	return protocols
}
