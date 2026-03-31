package mkringcontrol

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"syscall"
	"time"
)

const (
	// TransportUAPIVersion should match the kernel-side transport UAPI version.
	TransportUAPIVersion = 1
	// TransportMaxMessage should match the fixed-size message buffer in the kernel UAPI.
	TransportMaxMessage = 1024

	controlReceivePollTimeout = time.Second
	controlDefaultTimeout     = 5 * time.Second
)

var ErrTransportUAPINotImplemented = errors.New("mkring transport uapi is not implemented")

// TransportSend mirrors struct mkring_transport_send in mkring_transport_uapi.h.
type TransportSend struct {
	PeerKernelID uint16
	Channel      uint16
	MessageLen   uint32
	Message      [TransportMaxMessage]byte
}

// TransportRecv mirrors struct mkring_transport_recv in mkring_transport_uapi.h.
type TransportRecv struct {
	PeerKernelID uint16
	Channel      uint16
	TimeoutMS    uint32
	MessageLen   uint32
	Message      [TransportMaxMessage]byte
}

// HostTransportUAPI is the minimal host-side kernel/UAPI adapter required by the
// transport path. The kernel only provides send/recv semantics; ready
// handling and request/response matching remain in userspace.
type HostTransportUAPI interface {
	Send(ctx context.Context, req TransportSend) error
	Recv(ctx context.Context, req TransportRecv) (TransportRecv, error)
}

type pendingKey struct {
	peerKernelID uint16
	requestID    uint64
}

type pendingResult struct {
	message containerMessage
	err     error
}

type readyState struct {
	ready bool
	ch    chan struct{}
}

// UAPITransport is the syscall-backed control transport.
//
// It preserves the existing Transport interface so callers above Service do
// not need to change when the control bridge device is removed. The transport
// uses the generic send/recv syscall backend and keeps all control semantics in
// userspace.
type UAPITransport struct {
	uapi HostTransportUAPI

	startOnce sync.Once
	startErr  error

	mu       sync.Mutex
	ready    map[uint16]*readyState
	pending  map[pendingKey]chan pendingResult
	recvErr  error
	recvDone chan struct{}
}

func NewUAPITransport(uapi HostTransportUAPI) *UAPITransport {
	return &UAPITransport{
		uapi:     uapi,
		ready:    map[uint16]*readyState{},
		pending:  map[pendingKey]chan pendingResult{},
		recvDone: make(chan struct{}),
	}
}

func (t *UAPITransport) WaitReady(ctx context.Context, peerKernelID uint16, kernelID string, timeout time.Duration) error {
	if err := t.ensureStarted(); err != nil {
		return fmt.Errorf("wait ready peer=%d kernel=%s via transport uapi: %w", peerKernelID, kernelID, err)
	}

	state := t.ensureReadyState(peerKernelID)
	if state.ready {
		return nil
	}

	waitCtx, cancel, err := contextWithEffectiveTimeout(ctx, timeout, controlDefaultTimeout)
	if err != nil {
		return err
	}
	defer cancel()

	select {
	case <-state.ch:
		return nil
	case <-t.recvDone:
		return fmt.Errorf("wait ready peer=%d kernel=%s via transport uapi: %w", peerKernelID, kernelID, t.receiverError())
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
}

func (t *UAPITransport) ForcePeerReady(ctx context.Context, peerKernelID uint16, kernelID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := t.ensureStarted(); err != nil {
		return fmt.Errorf("force peer ready peer=%d kernel=%s via transport uapi: %w", peerKernelID, kernelID, err)
	}

	t.markReady(peerKernelID)
	return nil
}

func (t *UAPITransport) RoundTrip(ctx context.Context, peerKernelID uint16, req Envelope) (Envelope, error) {
	if err := t.ensureStarted(); err != nil {
		return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, err)
	}

	requestMsg, err := encodeEnvelopeMessage(req)
	if err != nil {
		return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, err)
	}

	requestID := envelopeTransportRequestID(req.ID)
	key := pendingKey{peerKernelID: peerKernelID, requestID: requestID}
	pendingCh := make(chan pendingResult, 1)
	if err := t.registerPending(key, pendingCh); err != nil {
		return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, err)
	}
	defer t.unregisterPending(key)

	sendReq := TransportSend{
		PeerKernelID: peerKernelID,
		Channel:      uint16(mkringContainerChannel),
		MessageLen:   uint32(len(requestMsg)),
	}
	copy(sendReq.Message[:], requestMsg)

	if err := t.uapi.Send(ctx, sendReq); err != nil {
		return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, err)
	}

	waitCtx, cancel, err := contextWithRequestTimeout(ctx, req, controlDefaultTimeout)
	if err != nil {
		return Envelope{}, err
	}
	defer cancel()

	select {
	case result := <-pendingCh:
		if result.err != nil {
			return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, result.err)
		}
		encodedResp, err := encodeStruct(result.message, containerMessageSize)
		if err != nil {
			return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, err)
		}
		resp, err := decodeEnvelopeMessage(req, encodedResp)
		if err != nil {
			return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, err)
		}
		return resp, nil
	case <-t.recvDone:
		return Envelope{}, fmt.Errorf("call peer=%d op=%s via transport uapi: %w", peerKernelID, req.Operation, t.receiverError())
	case <-waitCtx.Done():
		return Envelope{}, waitCtx.Err()
	}
}

func (t *UAPITransport) ensureStarted() error {
	t.startOnce.Do(func() {
		if t == nil || t.uapi == nil {
			t.startErr = ErrTransportUAPINotImplemented
			close(t.recvDone)
			return
		}
		go t.recvLoop()
	})
	return t.startErr
}

func (t *UAPITransport) recvLoop() {
	for {
		req := TransportRecv{
			Channel:   uint16(mkringContainerChannel),
			TimeoutMS: uint32(controlReceivePollTimeout / time.Millisecond),
		}
		resp, err := t.uapi.Recv(context.Background(), req)
		if err != nil {
			if errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			t.failReceiver(err)
			return
		}
		if resp.MessageLen == 0 || resp.MessageLen > TransportMaxMessage {
			continue
		}
		t.handleInbound(resp.PeerKernelID, resp.Message[:resp.MessageLen])
	}
}

func (t *UAPITransport) handleInbound(peerKernelID uint16, data []byte) {
	if len(data) != containerMessageSize {
		return
	}

	var msg containerMessage
	if err := decodeStruct(data, &msg); err != nil {
		return
	}
	if msg.Header.Magic != mkringContainerMagic ||
		msg.Header.Version != mkringContainerVersion ||
		msg.Header.Channel != mkringContainerChannel {
		return
	}

	switch msg.Header.Kind {
	case mkringContainerKindReady:
		t.markReady(peerKernelID)
	case mkringContainerKindResponse:
		if msg.Header.RequestID == 0 {
			return
		}
		t.completePending(pendingKey{
			peerKernelID: peerKernelID,
			requestID:    msg.Header.RequestID,
		}, pendingResult{message: msg})
	}
}

func (t *UAPITransport) ensureReadyState(peerKernelID uint16) *readyState {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.ready[peerKernelID]
	if ok {
		return state
	}
	state = &readyState{ch: make(chan struct{})}
	t.ready[peerKernelID] = state
	return state
}

func (t *UAPITransport) markReady(peerKernelID uint16) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.ready[peerKernelID]
	if !ok {
		state = &readyState{ch: make(chan struct{})}
		t.ready[peerKernelID] = state
	}
	if state.ready {
		return
	}
	state.ready = true
	close(state.ch)
}

func (t *UAPITransport) registerPending(key pendingKey, ch chan pendingResult) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.recvErr != nil {
		return t.recvErr
	}
	if _, exists := t.pending[key]; exists {
		return fmt.Errorf("duplicate pending request peer=%d request_id=%d", key.peerKernelID, key.requestID)
	}
	t.pending[key] = ch
	return nil
}

func (t *UAPITransport) unregisterPending(key pendingKey) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.pending, key)
}

func (t *UAPITransport) completePending(key pendingKey, result pendingResult) {
	t.mu.Lock()
	ch, ok := t.pending[key]
	if ok {
		delete(t.pending, key)
	}
	t.mu.Unlock()

	if ok {
		ch <- result
	}
}

func (t *UAPITransport) failReceiver(err error) {
	t.mu.Lock()
	if t.recvErr != nil {
		t.mu.Unlock()
		return
	}
	t.recvErr = err
	pending := t.pending
	t.pending = map[pendingKey]chan pendingResult{}
	close(t.recvDone)
	t.mu.Unlock()

	for _, ch := range pending {
		ch <- pendingResult{err: err}
	}
}

func (t *UAPITransport) receiverError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.recvErr != nil {
		return t.recvErr
	}
	return ErrTransportUAPINotImplemented
}

func contextWithRequestTimeout(ctx context.Context, req Envelope, fallback time.Duration) (context.Context, context.CancelFunc, error) {
	timeoutMS, err := timeoutMillisForRequest(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if timeoutMS == 0 {
		return contextWithEffectiveTimeout(ctx, 0, fallback)
	}
	return contextWithEffectiveTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond, fallback)
}

func contextWithEffectiveTimeout(ctx context.Context, timeout, fallback time.Duration) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	effective := timeout
	if effective <= 0 {
		effective = fallback
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil, context.DeadlineExceeded
		}
		if effective <= 0 || remaining < effective {
			effective = remaining
		}
	}
	if effective <= 0 {
		next, cancel := context.WithCancel(ctx)
		return next, cancel, nil
	}
	next, cancel := context.WithTimeout(ctx, effective)
	return next, cancel, nil
}

func encodeEnvelopeMessage(req Envelope) ([]byte, error) {
	msg, err := encodeRequestMessage(req)
	if err != nil {
		return nil, err
	}
	msg.Header.RequestID = envelopeTransportRequestID(req.ID)

	encoded, err := encodeStruct(msg, containerMessageSize)
	if err != nil {
		return nil, err
	}
	if len(encoded) > TransportMaxMessage {
		return nil, fmt.Errorf("encoded request exceeds transport uapi message size: got=%d max=%d", len(encoded), TransportMaxMessage)
	}
	return encoded, nil
}

func decodeEnvelopeMessage(req Envelope, msg []byte) (Envelope, error) {
	if len(msg) != containerMessageSize {
		return Envelope{}, fmt.Errorf("unexpected control response size: got=%d want=%d", len(msg), containerMessageSize)
	}

	expectedRequestID := envelopeTransportRequestID(req.ID)

	var response containerMessage
	if err := decodeStruct(msg, &response); err != nil {
		return Envelope{}, err
	}
	if response.Header.RequestID != expectedRequestID {
		return Envelope{}, fmt.Errorf("unexpected response request id: got=%d want=%d", response.Header.RequestID, expectedRequestID)
	}

	request, err := encodeRequestMessage(req)
	if err != nil {
		return Envelope{}, err
	}
	request.Header.RequestID = expectedRequestID

	return decodeResponseEnvelope(req, containerCall{
		Request:  request,
		Response: response,
	})
}

func envelopeTransportRequestID(id string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	sum := h.Sum64()
	if sum == 0 {
		return 1
	}
	return sum
}
