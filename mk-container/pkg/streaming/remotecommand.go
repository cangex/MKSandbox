package streaming

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	remoteCommandHeaderProtocolVersion = "X-Stream-Protocol-Version"
	remoteCommandHeaderUpgrade         = "Upgrade"
	remoteCommandHeaderConnection      = "Connection"
	remoteCommandUpgradeSPDY31         = "SPDY/3.1"
	remoteCommandHeaderStreamType      = "StreamType"
)

const (
	RemoteCommandProtocolV1 = "channel.k8s.io"
	RemoteCommandProtocolV2 = "v2.channel.k8s.io"
	RemoteCommandProtocolV3 = "v3.channel.k8s.io"
	RemoteCommandProtocolV4 = "v4.channel.k8s.io"
)

const (
	RemoteCommandStreamStdin  = "stdin"
	RemoteCommandStreamStdout = "stdout"
	RemoteCommandStreamStderr = "stderr"
	RemoteCommandStreamError  = "error"
	RemoteCommandStreamResize = "resize"
)

const (
	remoteCommandStartTimeout = 5 * time.Second
	remoteCommandErrorTimeout = 500 * time.Millisecond
)

var (
	DefaultRemoteCommandProtocols = []string{
		RemoteCommandProtocolV4,
		RemoteCommandProtocolV3,
		RemoteCommandProtocolV2,
		RemoteCommandProtocolV1,
	}
	ErrRemoteCommandNotImplemented = errors.New("remotecommand frontend is not implemented")
)

type ExecSession interface {
	Session() *Session
	DataPlane() DataPlaneSession
	Start(context.Context) error
	MarkExited(exitCode int32) error
	Close(context.Context) error
}

type RemoteCommandAdapter interface {
	ServeExec(ctx context.Context, w http.ResponseWriter, r *http.Request, exec ExecSession) error
}

func LooksLikeRemoteCommandRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.EqualFold(r.Header.Get(remoteCommandHeaderUpgrade), remoteCommandUpgradeSPDY31) {
		return true
	}
	if strings.Contains(strings.ToLower(r.Header.Get(remoteCommandHeaderConnection)), "upgrade") &&
		r.Header.Get(remoteCommandHeaderUpgrade) != "" {
		return true
	}
	if r.Header.Get(remoteCommandHeaderProtocolVersion) != "" {
		return true
	}
	return false
}
