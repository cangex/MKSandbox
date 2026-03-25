package streaming

import "context"

type DataPlaneSession interface {
	Output() <-chan []byte
	Exit() <-chan int32
	SendStdin(ctx context.Context, data []byte) error
	Close() error
}

type DataPlane interface {
	OpenSession(sessionID string, peerKernelID uint16) (DataPlaneSession, error)
}
