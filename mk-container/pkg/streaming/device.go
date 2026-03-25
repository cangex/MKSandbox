package streaming

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
)

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

type DeviceBridge struct {
	path     string
	startMu  sync.Mutex
	started  bool
	file     *os.File
	readErr  error
	writeMu  sync.Mutex
	mu       sync.Mutex
	sessions map[string]*DeviceSession
}

type DeviceSession struct {
	bridge       *DeviceBridge
	sessionID    string
	peerKernelID uint16
	outputCh     chan []byte
	exitCh       chan int32
	closeOnce    sync.Once
}

func NewDeviceBridge(path string) *DeviceBridge {
	return &DeviceBridge{
		path:     path,
		sessions: map[string]*DeviceSession{},
	}
}

func (b *DeviceBridge) OpenSession(sessionID string, peerKernelID uint16) (DataPlaneSession, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if err := b.ensureStarted(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.sessions[sessionID]; exists {
		return nil, fmt.Errorf("session %s already attached to data plane", sessionID)
	}

	sess := &DeviceSession{
		bridge:       b,
		sessionID:    sessionID,
		peerKernelID: peerKernelID,
		outputCh:     make(chan []byte, 32),
		exitCh:       make(chan int32, 1),
	}
	b.sessions[sessionID] = sess
	log.Printf("stream dataplane open session=%s peer_kernel_id=%d", sessionID, peerKernelID)
	return sess, nil
}

func (b *DeviceBridge) ensureStarted() error {
	b.startMu.Lock()
	defer b.startMu.Unlock()
	if b.started {
		return b.readErr
	}

	file, err := os.OpenFile(b.path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open stream device %s: %w", b.path, err)
	}
	b.file = file
	b.started = true

	go b.readLoop()
	return nil
}

func (b *DeviceBridge) readLoop() {
	for {
		packet, err := b.readPacket()
		if err != nil {
			log.Printf("stream dataplane read loop failed: %v", err)
			b.mu.Lock()
			b.readErr = err
			for id, sess := range b.sessions {
				delete(b.sessions, id)
				close(sess.outputCh)
				close(sess.exitCh)
			}
			b.mu.Unlock()
			return
		}
		b.dispatch(packet)
	}
}

func (b *DeviceBridge) readPacket() (deviceStreamPacket, error) {
	var packet deviceStreamPacket
	buf := make([]byte, streamPacketSize)
	if _, err := io.ReadFull(b.file, buf); err != nil {
		return packet, err
	}
	if err := binary.Read(bytes.NewReader(buf), binary.LittleEndian, &packet); err != nil {
		return packet, err
	}
	if packet.Message.Header.Magic != streamMagic {
		return packet, fmt.Errorf("unexpected stream magic: 0x%x", packet.Message.Header.Magic)
	}
	if packet.Message.Header.Version != streamVersion {
		return packet, fmt.Errorf("unexpected stream version: %d", packet.Message.Header.Version)
	}
	if packet.Message.Header.Channel != streamChannel {
		return packet, fmt.Errorf("unexpected stream channel: %d", packet.Message.Header.Channel)
	}
	if packet.Message.Header.PayloadLen > streamMaxPayload {
		return packet, fmt.Errorf("unexpected stream payload len: %d", packet.Message.Header.PayloadLen)
	}
	return packet, nil
}

func (b *DeviceBridge) dispatch(packet deviceStreamPacket) {
	sessionID := cString(packet.Message.Header.SessionID[:])

	b.mu.Lock()
	sess := b.sessions[sessionID]
	b.mu.Unlock()
	if sess == nil {
		return
	}

	payloadLen := int(packet.Message.Header.PayloadLen)
	payload := make([]byte, payloadLen)
	copy(payload, packet.Message.Payload[:payloadLen])

	switch packet.Message.Header.StreamType {
	case streamTypeOutput:
		log.Printf("stream dataplane recv output session=%s peer_kernel_id=%d bytes=%d", sessionID, packet.PeerKernelID, payloadLen)
		select {
		case sess.outputCh <- payload:
		default:
		}
	case streamTypeControl:
		var ctl deviceStreamControlExit
		if payloadLen != binary.Size(ctl) {
			return
		}
		if err := binary.Read(bytes.NewReader(payload), binary.LittleEndian, &ctl); err != nil {
			return
		}
		if ctl.Kind != streamControlExit {
			return
		}
		log.Printf("stream dataplane recv exit session=%s peer_kernel_id=%d exit_code=%d", sessionID, packet.PeerKernelID, ctl.ExitCode)
		select {
		case sess.exitCh <- ctl.ExitCode:
		default:
		}
	}
}

func (b *DeviceBridge) writePacket(packet deviceStreamPacket) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, packet); err != nil {
		return err
	}
	if buf.Len() != streamPacketSize {
		return fmt.Errorf("unexpected stream packet size: got=%d want=%d", buf.Len(), streamPacketSize)
	}
	_, err := b.file.Write(buf.Bytes())
	return err
}

func (s *DeviceSession) Output() <-chan []byte {
	return s.outputCh
}

func (s *DeviceSession) Exit() <-chan int32 {
	return s.exitCh
}

func (s *DeviceSession) SendStdin(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	for len(data) > 0 {
		chunk := data
		if len(chunk) > streamMaxPayload {
			chunk = chunk[:streamMaxPayload]
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		packet := deviceStreamPacket{
			PeerKernelID: s.peerKernelID,
			Message: deviceStreamMessage{
				Header: deviceStreamHeader{
					Magic:      streamMagic,
					Version:    streamVersion,
					Channel:    streamChannel,
					StreamType: streamTypeStdin,
					PayloadLen: uint32(len(chunk)),
				},
			},
		}
		copy(packet.Message.Header.SessionID[:], s.sessionID)
		copy(packet.Message.Payload[:], chunk)
		log.Printf("stream dataplane send stdin session=%s peer_kernel_id=%d bytes=%d", s.sessionID, s.peerKernelID, len(chunk))
		if err := s.bridge.writePacket(packet); err != nil {
			return err
		}
		data = data[len(chunk):]
	}
	return nil
}

func (s *DeviceSession) Close() error {
	s.closeOnce.Do(func() {
		s.bridge.mu.Lock()
		delete(s.bridge.sessions, s.sessionID)
		s.bridge.mu.Unlock()
	})
	return nil
}

func cString(buf []byte) string {
	if idx := bytes.IndexByte(buf, 0); idx >= 0 {
		buf = buf[:idx]
	}
	return string(buf)
}
