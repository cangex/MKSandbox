package streaming

import "bytes"

const (
	streamMagic        = 0x4d4b434e
	streamVersion      = 1
	streamChannel      = 3
	streamTypeStdin    = 1
	streamTypeOutput   = 2
	streamTypeControl  = 3
	streamControlExit  = 3
	streamMaxSessionID = 64
	streamMaxPayload   = 768
	streamPacketSize   = 856
)

type deviceStreamHeader struct {
	Magic      uint32
	Version    uint8
	Channel    uint8
	StreamType uint8
	Flags      uint8
	SessionSeq uint64
	SessionID  [streamMaxSessionID]byte
	PayloadLen uint32
}

type deviceStreamMessage struct {
	Header  deviceStreamHeader
	Payload [streamMaxPayload]byte
}

type deviceStreamPacket struct {
	PeerKernelID uint16
	Reserved0    uint16
	Message      deviceStreamMessage
}

type deviceStreamControlExit struct {
	Kind     uint32
	ExitCode int32
}

func cString(buf []byte) string {
	if idx := bytes.IndexByte(buf, 0); idx >= 0 {
		buf = buf[:idx]
	}
	return string(buf)
}
