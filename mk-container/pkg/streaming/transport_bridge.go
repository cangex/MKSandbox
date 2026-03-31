package streaming

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"syscall"
	"time"
)

const (
	transportMaxMessage      = 1024
	streamReceivePollTimeout = time.Second
	streamTransportSendOp    = 1
	streamTransportRecvOp    = 2
)

var (
	streamMessageSize                = binary.Size(deviceStreamMessage{})
	ErrStreamTransportNotImplemented = errors.New("mkring stream transport is not implemented")
)

// transportSend mirrors struct mkring_transport_send in the kernel UAPI.
type transportSend struct {
	PeerKernelID uint16
	Channel      uint16
	MessageLen   uint32
	Message      [transportMaxMessage]byte
}

// transportRecv mirrors struct mkring_transport_recv in the kernel UAPI.
type transportRecv struct {
	PeerKernelID uint16
	Channel      uint16
	TimeoutMS    uint32
	MessageLen   uint32
	Message      [transportMaxMessage]byte
}

type hostStreamTransportUAPI interface {
	Send(ctx context.Context, req transportSend) error
	Recv(ctx context.Context, req transportRecv) (transportRecv, error)
}

type TransportBridge struct {
	uapi hostStreamTransportUAPI

	startMu  sync.Mutex
	started  bool
	readErr  error
	recvDone chan struct{}

	writeMu  sync.Mutex
	mu       sync.Mutex
	sessions map[string]*TransportSession
}

type TransportSession struct {
	bridge       *TransportBridge
	sessionID    string
	peerKernelID uint16
	outputCh     chan []byte
	exitCh       chan int32
	closeOnce    sync.Once
}

func NewTransportBridge(uapi hostStreamTransportUAPI) *TransportBridge {
	return &TransportBridge{
		uapi:     uapi,
		recvDone: make(chan struct{}),
		sessions: map[string]*TransportSession{},
	}
}

func (b *TransportBridge) OpenSession(sessionID string, peerKernelID uint16) (DataPlaneSession, error) {
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

	sess := &TransportSession{
		bridge:       b,
		sessionID:    sessionID,
		peerKernelID: peerKernelID,
		outputCh:     make(chan []byte, 32),
		exitCh:       make(chan int32, 1),
	}
	b.sessions[sessionID] = sess
	log.Printf("stream dataplane open session=%s peer_kernel_id=%d transport=syscall", sessionID, peerKernelID)
	return sess, nil
}

func (b *TransportBridge) ensureStarted() error {
	b.startMu.Lock()
	defer b.startMu.Unlock()
	if b.started {
		return b.readErr
	}
	if b.uapi == nil {
		b.readErr = ErrStreamTransportNotImplemented
		close(b.recvDone)
		b.started = true
		return b.readErr
	}

	b.started = true
	go b.recvLoop()
	return nil
}

func (b *TransportBridge) recvLoop() {
	defer close(b.recvDone)

	for {
		req := transportRecv{
			Channel:   streamChannel,
			TimeoutMS: uint32(streamReceivePollTimeout / time.Millisecond),
		}
		resp, err := b.uapi.Recv(context.Background(), req)
		if err != nil {
			if errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			log.Printf("stream dataplane recv loop failed: %v", err)
			b.fail(err)
			return
		}
		if resp.MessageLen == 0 || int(resp.MessageLen) != streamMessageSize {
			continue
		}
		b.dispatch(resp.PeerKernelID, resp.Message[:resp.MessageLen])
	}
}

func (b *TransportBridge) fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.readErr = err
	for id, sess := range b.sessions {
		delete(b.sessions, id)
		close(sess.outputCh)
		close(sess.exitCh)
	}
}

func (b *TransportBridge) dispatch(peerKernelID uint16, data []byte) {
	msg, err := decodeStreamMessage(data)
	if err != nil {
		return
	}

	sessionID := cString(msg.Header.SessionID[:])
	b.mu.Lock()
	sess := b.sessions[sessionID]
	b.mu.Unlock()
	if sess == nil {
		return
	}

	payloadLen := int(msg.Header.PayloadLen)
	payload := make([]byte, payloadLen)
	copy(payload, msg.Payload[:payloadLen])

	switch msg.Header.StreamType {
	case streamTypeOutput:
		log.Printf("stream dataplane recv output session=%s peer_kernel_id=%d bytes=%d transport=syscall", sessionID, peerKernelID, payloadLen)
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
		log.Printf("stream dataplane recv exit session=%s peer_kernel_id=%d exit_code=%d transport=syscall", sessionID, peerKernelID, ctl.ExitCode)
		select {
		case sess.exitCh <- ctl.ExitCode:
		default:
		}
	}
}

func (b *TransportBridge) writeMessage(req transportSend) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.uapi.Send(context.Background(), req)
}

func (s *TransportSession) Output() <-chan []byte {
	return s.outputCh
}

func (s *TransportSession) Exit() <-chan int32 {
	return s.exitCh
}

func (s *TransportSession) SendStdin(ctx context.Context, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if err := s.bridge.ensureStarted(); err != nil {
		return err
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

		msg := deviceStreamMessage{
			Header: deviceStreamHeader{
				Magic:      streamMagic,
				Version:    streamVersion,
				Channel:    streamChannel,
				StreamType: streamTypeStdin,
				PayloadLen: uint32(len(chunk)),
			},
		}
		copy(msg.Header.SessionID[:], s.sessionID)
		copy(msg.Payload[:], chunk)

		encoded, err := encodeStreamMessage(msg)
		if err != nil {
			return err
		}

		req := transportSend{
			PeerKernelID: s.peerKernelID,
			Channel:      streamChannel,
			MessageLen:   uint32(len(encoded)),
		}
		copy(req.Message[:], encoded)

		log.Printf("stream dataplane send stdin session=%s peer_kernel_id=%d bytes=%d transport=syscall", s.sessionID, s.peerKernelID, len(chunk))
		if err := s.bridge.writeMessage(req); err != nil {
			return err
		}
		data = data[len(chunk):]
	}

	return nil
}

func (s *TransportSession) Close() error {
	s.closeOnce.Do(func() {
		s.bridge.mu.Lock()
		delete(s.bridge.sessions, s.sessionID)
		s.bridge.mu.Unlock()
	})
	return nil
}

func encodeStreamMessage(msg deviceStreamMessage) ([]byte, error) {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, msg); err != nil {
		return nil, err
	}
	if buf.Len() != streamMessageSize {
		return nil, fmt.Errorf("unexpected stream message size: got=%d want=%d", buf.Len(), streamMessageSize)
	}
	return buf.Bytes(), nil
}

func decodeStreamMessage(data []byte) (deviceStreamMessage, error) {
	var msg deviceStreamMessage
	if len(data) != streamMessageSize {
		return msg, io.ErrUnexpectedEOF
	}
	if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, &msg); err != nil {
		return msg, err
	}
	if msg.Header.Magic != streamMagic {
		return msg, fmt.Errorf("unexpected stream magic: 0x%x", msg.Header.Magic)
	}
	if msg.Header.Version != streamVersion {
		return msg, fmt.Errorf("unexpected stream version: %d", msg.Header.Version)
	}
	if msg.Header.Channel != streamChannel {
		return msg, fmt.Errorf("unexpected stream channel: %d", msg.Header.Channel)
	}
	if msg.Header.PayloadLen > streamMaxPayload {
		return msg, fmt.Errorf("unexpected stream payload len: %d", msg.Header.PayloadLen)
	}
	return msg, nil
}
