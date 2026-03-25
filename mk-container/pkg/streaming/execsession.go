package streaming

import (
	"context"
	"fmt"
	"time"
)

type preparedExecSession struct {
	server      *Server
	session     *Session
	dataSession DataPlaneSession
	started     bool
}

func (e *preparedExecSession) Session() *Session {
	if e == nil || e.session == nil {
		return nil
	}
	cp := *e.session
	return &cp
}

func (e *preparedExecSession) DataPlane() DataPlaneSession {
	if e == nil {
		return nil
	}
	return e.dataSession
}

func (e *preparedExecSession) Start(ctx context.Context) error {
	if e == nil {
		return fmt.Errorf("exec session is nil")
	}
	if e.started {
		return nil
	}
	if e.session != nil && e.session.Start != nil {
		if err := e.session.Start(ctx); err != nil {
			return err
		}
	}
	if err := e.server.manager.MarkRunning(e.session.ID); err != nil {
		return err
	}
	e.started = true
	return nil
}

func (e *preparedExecSession) MarkExited(exitCode int32) error {
	if e == nil {
		return fmt.Errorf("exec session is nil")
	}
	return e.server.manager.MarkExited(e.session.ID, exitCode)
}

func (e *preparedExecSession) Close(ctx context.Context) error {
	if e == nil {
		return nil
	}
	if e.dataSession != nil {
		_ = e.dataSession.Close()
	}
	if e.session != nil && e.session.CloseFn != nil {
		closeCtx := ctx
		if closeCtx == nil {
			var cancel context.CancelFunc
			closeCtx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
		}
		_ = e.session.CloseFn(closeCtx)
	}
	if e.session != nil {
		return e.server.manager.Close(e.session.ID)
	}
	return nil
}
