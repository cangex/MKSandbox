package streaming

import (
	"context"
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
	"time"
)

type fakeStreamTransportUAPI struct {
	sendReqs []transportSend
}

func (f *fakeStreamTransportUAPI) Send(_ context.Context, req transportSend) error {
	f.sendReqs = append(f.sendReqs, req)
	return nil
}

func (f *fakeStreamTransportUAPI) Recv(_ context.Context, _ transportRecv) (transportRecv, error) {
	return transportRecv{}, syscall.ETIMEDOUT
}

func TestTransportSessionSendStdinChunks(t *testing.T) {
	fake := &fakeStreamTransportUAPI{}
	bridge := NewTransportBridge(fake)
	bridge.started = true

	sessionIface, err := bridge.OpenSession("sess-1", 7)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	session := sessionIface.(*TransportSession)

	payload := make([]byte, streamMaxPayload*2+13)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	if err := session.SendStdin(context.Background(), payload); err != nil {
		t.Fatalf("SendStdin: %v", err)
	}

	if got, want := len(fake.sendReqs), 3; got != want {
		t.Fatalf("send count got=%d want=%d", got, want)
	}

	var total int
	for i, req := range fake.sendReqs {
		if req.PeerKernelID != 7 {
			t.Fatalf("req[%d] peer=%d want=7", i, req.PeerKernelID)
		}
		if req.Channel != streamChannel {
			t.Fatalf("req[%d] channel=%d want=%d", i, req.Channel, streamChannel)
		}
		if int(req.MessageLen) != streamMessageSize {
			t.Fatalf("req[%d] message_len=%d want=%d", i, req.MessageLen, streamMessageSize)
		}
		msg, err := decodeStreamMessage(req.Message[:req.MessageLen])
		if err != nil {
			t.Fatalf("decode req[%d]: %v", i, err)
		}
		if msg.Header.StreamType != streamTypeStdin {
			t.Fatalf("req[%d] stream_type=%d want=%d", i, msg.Header.StreamType, streamTypeStdin)
		}
		if gotID := cString(msg.Header.SessionID[:]); gotID != "sess-1" {
			t.Fatalf("req[%d] session_id=%q want=%q", i, gotID, "sess-1")
		}
		total += int(msg.Header.PayloadLen)
	}
	if total != len(payload) {
		t.Fatalf("payload bytes got=%d want=%d", total, len(payload))
	}
}

func TestTransportBridgeDispatchOutputAndExit(t *testing.T) {
	bridge := NewTransportBridge(nil)
	bridge.started = true

	sessionIface, err := bridge.OpenSession("sess-2", 9)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	session := sessionIface.(*TransportSession)

	outputMsg := deviceStreamMessage{
		Header: deviceStreamHeader{
			Magic:      streamMagic,
			Version:    streamVersion,
			Channel:    streamChannel,
			StreamType: streamTypeOutput,
			PayloadLen: 5,
		},
	}
	copy(outputMsg.Header.SessionID[:], "sess-2")
	copy(outputMsg.Payload[:], []byte("hello"))
	encodedOutput, err := encodeStreamMessage(outputMsg)
	if err != nil {
		t.Fatalf("encode output: %v", err)
	}
	bridge.dispatch(9, encodedOutput)

	select {
	case chunk := <-session.Output():
		if string(chunk) != "hello" {
			t.Fatalf("output=%q want=%q", string(chunk), "hello")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for output chunk")
	}

	var exitPayload deviceStreamControlExit
	exitPayload.Kind = streamControlExit
	exitPayload.ExitCode = 23
	var payloadBuf [8]byte
	binary.LittleEndian.PutUint32(payloadBuf[0:4], exitPayload.Kind)
	binary.LittleEndian.PutUint32(payloadBuf[4:8], uint32(exitPayload.ExitCode))

	exitMsg := deviceStreamMessage{
		Header: deviceStreamHeader{
			Magic:      streamMagic,
			Version:    streamVersion,
			Channel:    streamChannel,
			StreamType: streamTypeControl,
			PayloadLen: uint32(len(payloadBuf)),
		},
	}
	copy(exitMsg.Header.SessionID[:], "sess-2")
	copy(exitMsg.Payload[:], payloadBuf[:])
	encodedExit, err := encodeStreamMessage(exitMsg)
	if err != nil {
		t.Fatalf("encode exit: %v", err)
	}
	bridge.dispatch(9, encodedExit)

	select {
	case exitCode := <-session.Exit():
		if exitCode != 23 {
			t.Fatalf("exit_code=%d want=23", exitCode)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for exit")
	}
}

func TestTransportBridgeNotImplemented(t *testing.T) {
	bridge := NewTransportBridge(nil)
	if _, err := bridge.OpenSession("sess-3", 1); !errors.Is(err, ErrStreamTransportNotImplemented) {
		t.Fatalf("OpenSession err=%v want=%v", err, ErrStreamTransportNotImplemented)
	}
}
