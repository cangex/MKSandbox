package mkringcontrol

import (
	"context"
	"testing"
	"time"
)

func TestEncodeEnvelopeMessageCreateContainer(t *testing.T) {
	req, err := NewRequest("req-encode-1", 9, "kernel-a", OpCreateContainer, CreateContainerPayload{
		KernelID: "kernel-a",
		PodID:    "pod-a",
		Name:     "ctr-a",
		Image:    "busybox",
		Command:  []string{"/bin/sh"},
		Args:     []string{"-c", "echo ok"},
		LogPath:  "/tmp/ctr.log",
	})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	encoded, err := encodeEnvelopeMessage(req)
	if err != nil {
		t.Fatalf("encodeEnvelopeMessage: %v", err)
	}
	if got, want := len(encoded), containerMessageSize; got != want {
		t.Fatalf("encoded message size=%d want=%d", got, want)
	}

	var msg containerMessage
	if err := decodeStruct(encoded, &msg); err != nil {
		t.Fatalf("decodeStruct: %v", err)
	}
	if msg.Header.RequestID != envelopeTransportRequestID(req.ID) {
		t.Fatalf("request id=%d want=%d", msg.Header.RequestID, envelopeTransportRequestID(req.ID))
	}
	if msg.Header.Operation != mkringContainerOpCreate {
		t.Fatalf("operation=%d want=%d", msg.Header.Operation, mkringContainerOpCreate)
	}
	var payload containerCreateRequest
	if err := decodeStruct(msg.Payload[:containerCreateRequestSize], &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got := cString(payload.KernelID[:]); got != "kernel-a" {
		t.Fatalf("kernel id=%q", got)
	}
	if got := cString(payload.Name[:]); got != "ctr-a" {
		t.Fatalf("name=%q", got)
	}
}

func TestDecodeEnvelopeMessageCreateContainer(t *testing.T) {
	req, err := NewRequest("req-decode-1", 9, "kernel-a", OpCreateContainer, CreateContainerPayload{
		KernelID: "kernel-a",
		PodID:    "pod-a",
		Name:     "ctr-a",
		Image:    "busybox",
		LogPath:  "/tmp/ctr.log",
	})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	respMsg := containerMessage{}
	respMsg.Header = containerHeader{
		Magic:      mkringContainerMagic,
		Version:    mkringContainerVersion,
		Channel:    mkringContainerChannel,
		Kind:       mkringContainerKindResponse,
		Operation:  mkringContainerOpCreate,
		RequestID:  envelopeTransportRequestID(req.ID),
		PayloadLen: uint32(containerCreateResponseSize),
	}
	payload := containerCreateResponse{}
	if err := copyCString(payload.ContainerID[:], "ctr-created"); err != nil {
		t.Fatalf("copy container id: %v", err)
	}
	if err := copyCString(payload.ImageRef[:], "busybox:latest"); err != nil {
		t.Fatalf("copy image ref: %v", err)
	}
	payloadBytes, err := encodeStruct(payload, containerCreateResponseSize)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	copy(respMsg.Payload[:], payloadBytes)

	encoded, err := encodeStruct(respMsg, containerMessageSize)
	if err != nil {
		t.Fatalf("encode response msg: %v", err)
	}

	resp, err := decodeEnvelopeMessage(req, encoded)
	if err != nil {
		t.Fatalf("decodeEnvelopeMessage: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %+v", resp.Error)
	}
	var result CreateContainerResult
	if err := DecodePayload(resp, &result); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if result.ContainerID != "ctr-created" {
		t.Fatalf("container id=%q", result.ContainerID)
	}
	if result.ImageRef != "busybox:latest" {
		t.Fatalf("image ref=%q", result.ImageRef)
	}
}

func TestDecodeEnvelopeMessageError(t *testing.T) {
	req, err := NewRequest("req-error-1", 9, "kernel-a", OpStartContainer, ContainerControlPayload{
		KernelID:    "kernel-a",
		ContainerID: "ctr-a",
	})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	respMsg := containerMessage{}
	respMsg.Header = containerHeader{
		Magic:      mkringContainerMagic,
		Version:    mkringContainerVersion,
		Channel:    mkringContainerChannel,
		Kind:       mkringContainerKindResponse,
		Operation:  mkringContainerOpStart,
		RequestID:  envelopeTransportRequestID(req.ID),
		Status:     -2,
		PayloadLen: uint32(containerErrorPayloadSize),
	}
	payload := containerErrorPayload{ErrnoValue: -2}
	if err := copyCString(payload.Message[:], "missing container"); err != nil {
		t.Fatalf("copy error message: %v", err)
	}
	payloadBytes, err := encodeStruct(payload, containerErrorPayloadSize)
	if err != nil {
		t.Fatalf("encode error payload: %v", err)
	}
	copy(respMsg.Payload[:], payloadBytes)

	encoded, err := encodeStruct(respMsg, containerMessageSize)
	if err != nil {
		t.Fatalf("encode response msg: %v", err)
	}

	resp, err := decodeEnvelopeMessage(req, encoded)
	if err != nil {
		t.Fatalf("decodeEnvelopeMessage: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error response")
	}
	if resp.Error.Code != "errno_2" {
		t.Fatalf("error code=%q", resp.Error.Code)
	}
	if resp.Error.Message != "missing container" {
		t.Fatalf("error message=%q", resp.Error.Message)
	}
}

type fakeHostTransportUAPI struct {
	send func(TransportSend) error
	recv chan TransportRecv
}

func (f *fakeHostTransportUAPI) Send(_ context.Context, req TransportSend) error {
	if f.send != nil {
		return f.send(req)
	}
	return nil
}

func (f *fakeHostTransportUAPI) Recv(_ context.Context, _ TransportRecv) (TransportRecv, error) {
	msg := <-f.recv
	return msg, nil
}

func TestUAPITransportWaitReadyFromInboundMessage(t *testing.T) {
	backend := &fakeHostTransportUAPI{recv: make(chan TransportRecv, 1)}
	transport := NewUAPITransport(backend)

	ready := containerMessage{}
	ready.Header = containerHeader{
		Magic:      mkringContainerMagic,
		Version:    mkringContainerVersion,
		Channel:    mkringContainerChannel,
		Kind:       mkringContainerKindReady,
		Operation:  mkringContainerOpNone,
		PayloadLen: uint32(binarySizeOfReadyPayload()),
	}
	encoded, err := encodeStruct(ready, containerMessageSize)
	if err != nil {
		t.Fatalf("encodeStruct: %v", err)
	}

	var recvReq TransportRecv
	recvReq.PeerKernelID = 7
	recvReq.Channel = uint16(mkringContainerChannel)
	recvReq.MessageLen = uint32(len(encoded))
	copy(recvReq.Message[:], encoded)
	backend.recv <- recvReq

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := transport.WaitReady(ctx, 7, "kernel-7", 0); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestUAPITransportRoundTripMatchesResponseInUserspace(t *testing.T) {
	req, err := NewRequest("req-roundtrip-1", 9, "kernel-a", OpCreateContainer, CreateContainerPayload{
		KernelID: "kernel-a",
		PodID:    "pod-a",
		Name:     "ctr-a",
		Image:    "busybox",
		LogPath:  "/tmp/ctr.log",
	})
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	backend := &fakeHostTransportUAPI{recv: make(chan TransportRecv, 1)}
	backend.send = func(call TransportSend) error {
		var sent containerMessage
		if err := decodeStruct(call.Message[:call.MessageLen], &sent); err != nil {
			return err
		}

		respMsg := containerMessage{}
		respMsg.Header = containerHeader{
			Magic:      mkringContainerMagic,
			Version:    mkringContainerVersion,
			Channel:    mkringContainerChannel,
			Kind:       mkringContainerKindResponse,
			Operation:  mkringContainerOpCreate,
			RequestID:  sent.Header.RequestID,
			PayloadLen: uint32(containerCreateResponseSize),
		}
		payload := containerCreateResponse{}
		if err := copyCString(payload.ContainerID[:], "ctr-created"); err != nil {
			return err
		}
		if err := copyCString(payload.ImageRef[:], "busybox:latest"); err != nil {
			return err
		}
		payloadBytes, err := encodeStruct(payload, containerCreateResponseSize)
		if err != nil {
			return err
		}
		copy(respMsg.Payload[:], payloadBytes)

		encoded, err := encodeStruct(respMsg, containerMessageSize)
		if err != nil {
			return err
		}

		var recvReq TransportRecv
		recvReq.PeerKernelID = call.PeerKernelID
		recvReq.Channel = uint16(mkringContainerChannel)
		recvReq.MessageLen = uint32(len(encoded))
		copy(recvReq.Message[:], encoded)
		backend.recv <- recvReq
		return nil
	}

	transport := NewUAPITransport(backend)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resp, err := transport.RoundTrip(ctx, 9, req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	var result CreateContainerResult
	if err := DecodePayload(resp, &result); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if result.ContainerID != "ctr-created" {
		t.Fatalf("container id=%q", result.ContainerID)
	}
	if result.ImageRef != "busybox:latest" {
		t.Fatalf("image ref=%q", result.ImageRef)
	}
}

func binarySizeOfReadyPayload() int {
	return 20
}
