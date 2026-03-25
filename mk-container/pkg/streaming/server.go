package streaming

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	manager       *Manager
	baseURL       string
	data          DataPlane
	remoteCommand RemoteCommandAdapter
}

func NewServer(manager *Manager, baseURL string) *Server {
	if manager == nil {
		manager = NewManager()
	}
	return &Server{
		manager: manager,
		baseURL: strings.TrimRight(baseURL, "/"),
	}
}

func (s *Server) SetDataPlane(data DataPlane) {
	s.data = data
}

func (s *Server) SetRemoteCommandAdapter(adapter RemoteCommandAdapter) {
	s.remoteCommand = adapter
}

func (s *Server) RegisterTTYSession(sess Session) (string, error) {
	if err := s.manager.Add(sess); err != nil {
		return "", err
	}
	return s.manager.IssueToken(sess.ID, 0)
}

func (s *Server) ExecURL(token string) string {
	if s.baseURL == "" {
		return "/exec/" + token
	}
	return s.baseURL + "/exec/" + token
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := s.execTokenFromRequest(r)
	if !ok {
		http.Error(w, fmt.Sprintf("tty exec streaming is not implemented for %s", r.URL.Path), http.StatusNotFound)
		return
	}

	execSession, err := s.prepareExecSession(token)
	if err != nil {
		s.writeExecError(w, err)
		return
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer closeCancel()
		_ = execSession.Close(closeCtx)
	}()

	if LooksLikeRemoteCommandRequest(r) && s.remoteCommand != nil {
		if err := s.remoteCommand.ServeExec(r.Context(), w, r, execSession); err == nil {
			return
		} else if !errors.Is(err, ErrRemoteCommandNotImplemented) {
			http.Error(w, fmt.Sprintf("remotecommand exec failed: %v", err), http.StatusBadGateway)
			return
		}
	}

	s.serveRawExec(w, r, execSession)
}

func (s *Server) execTokenFromRequest(r *http.Request) (string, bool) {
	if r == nil || r.URL.Path == "" || !strings.HasPrefix(r.URL.Path, "/exec/") {
		return "", false
	}
	token := strings.TrimPrefix(r.URL.Path, "/exec/")
	if token == "" {
		return "", false
	}
	return token, true
}

func (s *Server) prepareExecSession(token string) (ExecSession, error) {
	sess, ok := s.manager.ConsumeToken(token)
	if !ok {
		return nil, fmt.Errorf("invalid or expired exec token")
	}
	if err := s.manager.Attach(sess.ID); err != nil {
		return nil, err
	}

	prepared := &preparedExecSession{
		server:  s,
		session: sess,
	}
	if s.data != nil {
		dataSession, err := s.data.OpenSession(sess.ID, sess.PeerKernelID)
		if err != nil {
			_ = prepared.Close(context.Background())
			return nil, fmt.Errorf("exec stream attach failed: %w", err)
		}
		prepared.dataSession = dataSession
	}
	return prepared, nil
}

func (s *Server) writeExecError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		return
	case strings.Contains(err.Error(), "invalid or expired exec token"):
		http.Error(w, err.Error(), http.StatusNotFound)
	case strings.Contains(err.Error(), "already"):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func (s *Server) serveRawExec(w http.ResponseWriter, r *http.Request, execSession ExecSession) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := execSession.Start(ctx); err != nil {
		http.Error(w, fmt.Sprintf("exec start failed: %v", err), http.StatusBadGateway)
		return
	}

	dataSession := execSession.DataPlane()
	if dataSession == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte("tty exec streaming data plane is not wired yet\n"))
		return
	}

	if body := r.Body; body != nil {
		go func() {
			defer body.Close()
			buf := make([]byte, 4096)
			for {
				n, err := body.Read(buf)
				if n > 0 {
					if sendErr := dataSession.SendStdin(r.Context(), buf[:n]); sendErr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-dataSession.Output():
			if !ok {
				return
			}
			if len(chunk) == 0 {
				continue
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case exitCode, ok := <-dataSession.Exit():
			if ok {
				_ = execSession.MarkExited(exitCode)
				if flusher != nil {
					flusher.Flush()
				}
			}
			return
		}
	}
}
