package streaming

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/spdystream"
)

type fakeRemoteCommandAdapter struct {
	called    bool
	sessionID string
	err       error
}

func (a *fakeRemoteCommandAdapter) ServeExec(_ context.Context, w http.ResponseWriter, _ *http.Request, exec ExecSession) error {
	a.called = true
	if sess := exec.Session(); sess != nil {
		a.sessionID = sess.ID
	}
	if a.err != nil {
		return a.err
	}
	w.WriteHeader(http.StatusSwitchingProtocols)
	return nil
}

type hijackableRecorder struct {
	header      http.Header
	code        int
	body        bytes.Buffer
	conn        net.Conn
	hijackReady chan struct{}
}

func newHijackableRecorder(conn net.Conn) *hijackableRecorder {
	return &hijackableRecorder{
		header:      make(http.Header),
		conn:        conn,
		hijackReady: make(chan struct{}),
	}
}

func (r *hijackableRecorder) Header() http.Header {
	return r.header
}

func (r *hijackableRecorder) WriteHeader(statusCode int) {
	r.code = statusCode
}

func (r *hijackableRecorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}

func (r *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	select {
	case <-r.hijackReady:
	default:
		close(r.hijackReady)
	}
	return r.conn, bufio.NewReadWriter(bufio.NewReader(r.conn), bufio.NewWriter(r.conn)), nil
}

type fakeDataPlane struct {
	session *fakeDataPlaneSession
}

type fakeDataPlaneSession struct {
	outputCh  chan []byte
	exitCh    chan int32
	stdinBuf  bytes.Buffer
	closed    bool
	stdinDone chan struct{}
}

func (p *fakeDataPlane) OpenSession(_ string, _ uint16) (DataPlaneSession, error) {
	if p.session == nil {
		p.session = &fakeDataPlaneSession{
			outputCh:  make(chan []byte, 4),
			exitCh:    make(chan int32, 1),
			stdinDone: make(chan struct{}),
		}
	}
	return p.session, nil
}

func (s *fakeDataPlaneSession) Output() <-chan []byte {
	return s.outputCh
}

func (s *fakeDataPlaneSession) Exit() <-chan int32 {
	return s.exitCh
}

func (s *fakeDataPlaneSession) SendStdin(_ context.Context, data []byte) error {
	_, err := s.stdinBuf.Write(data)
	select {
	case <-s.stdinDone:
	default:
		close(s.stdinDone)
	}
	return err
}

func (s *fakeDataPlaneSession) Close() error {
	if !s.closed {
		s.closed = true
		close(s.outputCh)
		close(s.exitCh)
	}
	return nil
}

func TestRegisterTTYSessionReturnsExecURLToken(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")

	token, err := srv.RegisterTTYSession(Session{
		ID:          "sess-1",
		ContainerID: "ctr-1",
		KernelID:    "kernel-1",
		TTY:         true,
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}
	if token == "" {
		t.Fatalf("expected non-empty token")
	}

	url := srv.ExecURL(token)
	if !strings.HasPrefix(url, "http://127.0.0.1:10010/exec/") {
		t.Fatalf("unexpected exec url: %s", url)
	}
}

func TestServeHTTPStartsAndClosesSession(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")
	started := false
	closed := false

	token, err := srv.RegisterTTYSession(Session{
		ID:          "sess-2",
		ContainerID: "ctr-2",
		KernelID:    "kernel-2",
		TTY:         true,
		Start: func(context.Context) error {
			started = true
			return nil
		},
		CloseFn: func(context.Context) error {
			closed = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/exec/"+token, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("unexpected status: got=%d want=%d", res.StatusCode, http.StatusNotImplemented)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "not wired yet") {
		t.Fatalf("unexpected body: %s", string(body))
	}
	if !started {
		t.Fatalf("expected session start to be invoked")
	}
	if !closed {
		t.Fatalf("expected session close to be invoked")
	}

	sess, ok := srv.manager.Get("sess-2")
	if !ok {
		t.Fatalf("expected session to still be tracked")
	}
	if sess.State != SessionClosed {
		t.Fatalf("unexpected session state: got=%s want=%s", sess.State, SessionClosed)
	}
}

func TestServeHTTPStartFailureClosesSession(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")
	closed := false

	token, err := srv.RegisterTTYSession(Session{
		ID:          "sess-3",
		ContainerID: "ctr-3",
		KernelID:    "kernel-3",
		TTY:         true,
		Start: func(context.Context) error {
			return io.EOF
		},
		CloseFn: func(context.Context) error {
			closed = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/exec/"+token, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unexpected status: got=%d want=%d", rec.Code, http.StatusBadGateway)
	}
	if !closed {
		t.Fatalf("expected close to be invoked on start failure")
	}

	sess, ok := srv.manager.Get("sess-3")
	if !ok {
		t.Fatalf("expected session to still be tracked")
	}
	if sess.State != SessionClosed {
		t.Fatalf("unexpected session state: got=%s want=%s", sess.State, SessionClosed)
	}
}

func TestServeHTTPStreamsOutputAndMarksExit(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")
	dataPlane := &fakeDataPlane{}
	srv.SetDataPlane(dataPlane)
	started := false
	closed := false

	token, err := srv.RegisterTTYSession(Session{
		ID:           "sess-4",
		ContainerID:  "ctr-4",
		KernelID:     "kernel-4",
		PeerKernelID: 1,
		TTY:          true,
		Start: func(context.Context) error {
			started = true
			go func() {
				<-dataPlane.session.stdinDone
				dataPlane.session.outputCh <- []byte("hello\n")
				dataPlane.session.exitCh <- 7
			}()
			return nil
		},
		CloseFn: func(context.Context) error {
			closed = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/exec/"+token, strings.NewReader("stdin-data"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: got=%d want=%d", res.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "hello\n" {
		t.Fatalf("unexpected streamed body: %q", string(body))
	}
	if !started {
		t.Fatalf("expected start to be invoked")
	}
	if !closed {
		t.Fatalf("expected close to be invoked")
	}
	if got := dataPlane.session.stdinBuf.String(); got != "stdin-data" {
		t.Fatalf("unexpected stdin data: %q", got)
	}

	sess, ok := srv.manager.Get("sess-4")
	if !ok {
		t.Fatalf("expected session to still be tracked")
	}
	if sess.State != SessionClosed {
		t.Fatalf("unexpected session state: got=%s want=%s", sess.State, SessionClosed)
	}
	if sess.ExitCode == nil || *sess.ExitCode != 7 {
		t.Fatalf("unexpected session exit code: %#v", sess.ExitCode)
	}
}

func TestServeHTTPDispatchesRemoteCommandAdapter(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")
	adapter := &fakeRemoteCommandAdapter{}
	srv.SetRemoteCommandAdapter(adapter)

	token, err := srv.RegisterTTYSession(Session{
		ID:          "sess-spdy-1",
		ContainerID: "ctr-spdy-1",
		KernelID:    "kernel-spdy-1",
		TTY:         true,
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/exec/"+token, nil)
	req.Header.Set(remoteCommandHeaderUpgrade, remoteCommandUpgradeSPDY31)
	req.Header.Set(remoteCommandHeaderConnection, "Upgrade")
	req.Header.Set(remoteCommandHeaderProtocolVersion, RemoteCommandProtocolV4)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if !adapter.called {
		t.Fatalf("expected remotecommand adapter to be invoked")
	}
	if adapter.sessionID != "sess-spdy-1" {
		t.Fatalf("unexpected session id: got=%s want=%s", adapter.sessionID, "sess-spdy-1")
	}
	if rec.Code != http.StatusSwitchingProtocols {
		t.Fatalf("unexpected status: got=%d want=%d", rec.Code, http.StatusSwitchingProtocols)
	}
}

func TestSPDYRemoteCommandAdapterNegotiatesProtocol(t *testing.T) {
	adapter := NewSPDYRemoteCommandAdapter(nil)
	req := httptest.NewRequest(http.MethodPost, "/exec/token", nil)
	req.Header.Set(remoteCommandHeaderUpgrade, remoteCommandUpgradeSPDY31)
	req.Header.Set(remoteCommandHeaderConnection, "Upgrade")
	req.Header.Set(remoteCommandHeaderProtocolVersion, strings.Join([]string{
		RemoteCommandProtocolV4,
		RemoteCommandProtocolV3,
	}, ", "))

	rec := httptest.NewRecorder()
	exec := &preparedExecSession{
		server:      NewServer(nil, "http://127.0.0.1:10010"),
		session:     &Session{ID: "sess-spdy-2", TTY: true},
		dataSession: &fakeDataPlaneSession{outputCh: make(chan []byte), exitCh: make(chan int32), stdinDone: make(chan struct{})},
	}

	err := adapter.ServeExec(context.Background(), rec, req, exec)
	if err == nil {
		t.Fatalf("expected ServeExec to fail without hijacker support")
	}
	if !strings.Contains(err.Error(), "hijacking") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSPDYRemoteCommandAdapterRejectsUnsupportedProtocol(t *testing.T) {
	adapter := NewSPDYRemoteCommandAdapter(nil)
	req := httptest.NewRequest(http.MethodPost, "/exec/token", nil)
	req.Header.Set(remoteCommandHeaderUpgrade, remoteCommandUpgradeSPDY31)
	req.Header.Set(remoteCommandHeaderConnection, "Upgrade")
	req.Header.Set(remoteCommandHeaderProtocolVersion, "v9.channel.k8s.io")

	rec := httptest.NewRecorder()
	exec := &preparedExecSession{
		server:  NewServer(nil, "http://127.0.0.1:10010"),
		session: &Session{ID: "sess-spdy-3", TTY: true},
	}

	if err := adapter.ServeExec(context.Background(), rec, req, exec); err != nil {
		t.Fatalf("ServeExec returned error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: got=%d want=%d", rec.Code, http.StatusBadRequest)
	}
}

func TestSPDYRemoteCommandAdapterStreamsStdinStdout(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")
	dataPlane := &fakeDataPlane{}
	srv.SetDataPlane(dataPlane)
	srv.SetRemoteCommandAdapter(NewSPDYRemoteCommandAdapter(nil))

	token, err := srv.RegisterTTYSession(Session{
		ID:           "sess-spdy-stream-1",
		ContainerID:  "ctr-spdy-stream-1",
		KernelID:     "kernel-spdy-stream-1",
		PeerKernelID: 1,
		TTY:          true,
		Start: func(context.Context) error {
			go func() {
				<-dataPlane.session.stdinDone
				dataPlane.session.outputCh <- []byte("hello over spdy\n")
				dataPlane.session.exitCh <- 0
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	rec := newHijackableRecorder(serverConn)

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:10010/exec/"+token, nil)
	req.Header.Set(remoteCommandHeaderUpgrade, remoteCommandUpgradeSPDY31)
	req.Header.Set(remoteCommandHeaderConnection, "Upgrade")
	req.Header.Set(remoteCommandHeaderProtocolVersion, RemoteCommandProtocolV4)

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		srv.ServeHTTP(rec, req)
	}()

	<-rec.hijackReady
	if rec.code != http.StatusSwitchingProtocols {
		t.Fatalf("unexpected status: got=%d want=%d", rec.code, http.StatusSwitchingProtocols)
	}
	if got := rec.header.Get(remoteCommandHeaderProtocolVersion); got != RemoteCommandProtocolV4 {
		t.Fatalf("unexpected negotiated protocol: got=%s want=%s", got, RemoteCommandProtocolV4)
	}

	conn, err := spdystream.NewConnection(clientConn, false)
	if err != nil {
		t.Fatalf("spdy connection: %v", err)
	}
	defer conn.Close()
	go conn.Serve(spdystream.NoOpStreamHandler)

	stdoutStream, err := conn.CreateStream(http.Header{
		remoteCommandHeaderStreamType: []string{RemoteCommandStreamStdout},
	}, nil, false)
	if err != nil {
		t.Fatalf("create stdout stream: %v", err)
	}
	if err := stdoutStream.Wait(); err != nil {
		t.Fatalf("wait stdout reply: %v", err)
	}

	stdinStream, err := conn.CreateStream(http.Header{
		remoteCommandHeaderStreamType: []string{RemoteCommandStreamStdin},
	}, nil, false)
	if err != nil {
		t.Fatalf("create stdin stream: %v", err)
	}
	if err := stdinStream.Wait(); err != nil {
		t.Fatalf("wait stdin reply: %v", err)
	}

	if _, err := stdinStream.Write([]byte("stdin-data")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = stdinStream.Close()

	buf := make([]byte, 64)
	n, err := stdoutStream.Read(buf)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if got := string(buf[:n]); got != "hello over spdy\n" {
		t.Fatalf("unexpected stdout: %q", got)
	}
	if got := dataPlane.session.stdinBuf.String(); got != "stdin-data" {
		t.Fatalf("unexpected stdin data: %q", got)
	}

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("server did not finish spdy exec session")
	}
}

func TestSPDYRemoteCommandAdapterHandlesResizeStream(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")
	dataPlane := &fakeDataPlane{}
	srv.SetDataPlane(dataPlane)
	srv.SetRemoteCommandAdapter(NewSPDYRemoteCommandAdapter(nil))

	resizeCh := make(chan ResizeEvent, 1)
	resizeApplied := make(chan struct{})
	token, err := srv.RegisterTTYSession(Session{
		ID:           "sess-spdy-resize-1",
		ContainerID:  "ctr-spdy-resize-1",
		KernelID:     "kernel-spdy-resize-1",
		PeerKernelID: 1,
		TTY:          true,
		Start: func(context.Context) error {
			go func() {
				dataPlane.session.outputCh <- []byte("prompt")
				<-resizeApplied
				dataPlane.session.exitCh <- 0
			}()
			return nil
		},
		ResizeFn: func(_ context.Context, ev ResizeEvent) error {
			select {
			case resizeCh <- ev:
			default:
			}
			select {
			case <-resizeApplied:
			default:
				close(resizeApplied)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	rec := newHijackableRecorder(serverConn)

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:10010/exec/"+token, nil)
	req.Header.Set(remoteCommandHeaderUpgrade, remoteCommandUpgradeSPDY31)
	req.Header.Set(remoteCommandHeaderConnection, "Upgrade")
	req.Header.Set(remoteCommandHeaderProtocolVersion, RemoteCommandProtocolV4)

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		srv.ServeHTTP(rec, req)
	}()

	<-rec.hijackReady
	conn, err := spdystream.NewConnection(clientConn, false)
	if err != nil {
		t.Fatalf("spdy connection: %v", err)
	}
	defer conn.Close()
	go conn.Serve(spdystream.NoOpStreamHandler)

	stdoutStream, err := conn.CreateStream(http.Header{
		remoteCommandHeaderStreamType: []string{RemoteCommandStreamStdout},
	}, nil, false)
	if err != nil {
		t.Fatalf("create stdout stream: %v", err)
	}
	if err := stdoutStream.Wait(); err != nil {
		t.Fatalf("wait stdout reply: %v", err)
	}

	resizeStream, err := conn.CreateStream(http.Header{
		remoteCommandHeaderStreamType: []string{RemoteCommandStreamResize},
	}, nil, false)
	if err != nil {
		t.Fatalf("create resize stream: %v", err)
	}
	if err := resizeStream.Wait(); err != nil {
		t.Fatalf("wait resize reply: %v", err)
	}

	if _, err := resizeStream.Write([]byte(`{"width":120,"height":40}`)); err != nil {
		t.Fatalf("write resize: %v", err)
	}

	select {
	case ev := <-resizeCh:
		if ev.Width != 120 || ev.Height != 40 {
			t.Fatalf("unexpected resize event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("did not receive resize event")
	}

	buf := make([]byte, 16)
	if _, err := stdoutStream.Read(buf); err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("server did not finish spdy exec session")
	}
}

func TestSPDYRemoteCommandAdapterWritesStructuredExitError(t *testing.T) {
	srv := NewServer(nil, "http://127.0.0.1:10010")
	dataPlane := &fakeDataPlane{}
	srv.SetDataPlane(dataPlane)
	srv.SetRemoteCommandAdapter(NewSPDYRemoteCommandAdapter(nil))

	token, err := srv.RegisterTTYSession(Session{
		ID:           "sess-spdy-exit-1",
		ContainerID:  "ctr-spdy-exit-1",
		KernelID:     "kernel-spdy-exit-1",
		PeerKernelID: 1,
		TTY:          true,
		Start: func(context.Context) error {
			go func() {
				dataPlane.session.outputCh <- []byte("prompt")
				dataPlane.session.exitCh <- 23
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("register session: %v", err)
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	rec := newHijackableRecorder(serverConn)

	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:10010/exec/"+token, nil)
	req.Header.Set(remoteCommandHeaderUpgrade, remoteCommandUpgradeSPDY31)
	req.Header.Set(remoteCommandHeaderConnection, "Upgrade")
	req.Header.Set(remoteCommandHeaderProtocolVersion, RemoteCommandProtocolV4)

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		srv.ServeHTTP(rec, req)
	}()

	<-rec.hijackReady
	conn, err := spdystream.NewConnection(clientConn, false)
	if err != nil {
		t.Fatalf("spdy connection: %v", err)
	}
	defer conn.Close()
	go conn.Serve(spdystream.NoOpStreamHandler)

	stdoutStream, err := conn.CreateStream(http.Header{
		remoteCommandHeaderStreamType: []string{RemoteCommandStreamStdout},
	}, nil, false)
	if err != nil {
		t.Fatalf("create stdout stream: %v", err)
	}
	if err := stdoutStream.Wait(); err != nil {
		t.Fatalf("wait stdout reply: %v", err)
	}

	errorStream, err := conn.CreateStream(http.Header{
		remoteCommandHeaderStreamType: []string{RemoteCommandStreamError},
	}, nil, false)
	if err != nil {
		t.Fatalf("create error stream: %v", err)
	}
	if err := errorStream.Wait(); err != nil {
		t.Fatalf("wait error reply: %v", err)
	}

	buf := make([]byte, 16)
	if _, err := stdoutStream.Read(buf); err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	payload, err := io.ReadAll(errorStream)
	if err != nil {
		t.Fatalf("read error stream: %v", err)
	}
	body := string(payload)
	if !strings.Contains(body, "\"reason\":\"NonZeroExitCode\"") {
		t.Fatalf("unexpected error payload: %q", body)
	}
	if !strings.Contains(body, "\"message\":\"23\"") {
		t.Fatalf("unexpected error payload: %q", body)
	}

	select {
	case <-doneCh:
	case <-time.After(3 * time.Second):
		t.Fatalf("server did not finish spdy exec session")
	}
}
