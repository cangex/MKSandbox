package streaming

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func NewToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (m *Manager) IssueToken(sessionID string, _ time.Duration) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[sessionID]; !ok {
		return "", fmt.Errorf("session %s not found", sessionID)
	}
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	m.tokens[token] = sessionID
	return token, nil
}

func (m *Manager) ConsumeToken(token string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessionID, ok := m.tokens[token]
	if !ok {
		return nil, false
	}
	delete(m.tokens, token)
	sess, ok := m.sessions[sessionID]
	if !ok {
		return nil, false
	}
	cp := *sess
	return &cp, true
}
