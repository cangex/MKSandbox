package streaming

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type SessionState string

const (
	SessionPrepared SessionState = "prepared"
	SessionAttached SessionState = "attached"
	SessionRunning  SessionState = "running"
	SessionExited   SessionState = "exited"
	SessionClosed   SessionState = "closed"
)

type Session struct {
	ID           string
	ContainerID  string
	KernelID     string
	PeerKernelID uint16
	TTY          bool
	State        SessionState
	CreatedAt    time.Time
	ExitCode     *int32
	Start        func(context.Context) error
	ResizeFn     func(context.Context, ResizeEvent) error
	CloseFn      func(context.Context) error
}

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	tokens   map[string]string
}

func NewManager() *Manager {
	return &Manager{
		sessions: map[string]*Session{},
		tokens:   map[string]string{},
	}
}

func (m *Manager) Add(sess Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess.ID == "" {
		return fmt.Errorf("session id is required")
	}
	if _, exists := m.sessions[sess.ID]; exists {
		return fmt.Errorf("session %s already exists", sess.ID)
	}
	cp := sess
	if cp.State == "" {
		cp.State = SessionPrepared
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	m.sessions[sess.ID] = &cp
	return nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	cp := *sess
	return &cp, true
}

func (m *Manager) Attach(sessionID string) error {
	return m.updateState(sessionID, SessionAttached)
}

func (m *Manager) MarkRunning(sessionID string) error {
	return m.updateState(sessionID, SessionRunning)
}

func (m *Manager) MarkExited(sessionID string, exitCode int32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	sess.State = SessionExited
	sess.ExitCode = &exitCode
	return nil
}

func (m *Manager) Close(sessionID string) error {
	return m.updateState(sessionID, SessionClosed)
}

func (m *Manager) updateState(sessionID string, state SessionState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	sess.State = state
	return nil
}
