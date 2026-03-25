package service

import "errors"

var ErrExecTTYNotImplemented = errors.New("tty exec control-plane is not wired yet")
