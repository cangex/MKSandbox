package mkringcontrol

import (
	"context"
	"testing"
	"time"
	"unsafe"
)

type fakeDeviceFile struct {
	fd     uintptr
	closed bool
}

func (f *fakeDeviceFile) Fd() uintptr {
	return f.fd
}

func (f *fakeDeviceFile) Close() error {
	f.closed = true
	return nil
}

func argBytes(arg uintptr, size int) []byte {
	return (*[containerCallSize]byte)(unsafe.Pointer(arg))[:size:size]
}

func TestDeviceTransportWaitReady(t *testing.T) {
	file := &fakeDeviceFile{fd: 41}
	transport := NewDeviceTransport("/dev/mkring_container_bridge")
	transport.open = func(path string) (deviceFile, error) {
		if path != "/dev/mkring_container_bridge" {
			t.Fatalf("unexpected device path: %s", path)
		}
		return file, nil
	}
	transport.ioctl = func(fd uintptr, req uintptr, arg uintptr) error {
		if fd != file.fd {
			t.Fatalf("unexpected fd: %d", fd)
		}
		if req != ioctlWaitReady {
			t.Fatalf("unexpected ioctl request: %#x", req)
		}

		buf := argBytes(arg, containerWaitReadySize)
		var wait containerWaitReady
		if err := decodeStruct(buf, &wait); err != nil {
			t.Fatalf("decode wait-ready request: %v", err)
		}
		if wait.PeerKernelID != 7 {
			t.Fatalf("unexpected peer kernel id: %d", wait.PeerKernelID)
		}
		if wait.TimeoutMS != 2500 {
			t.Fatalf("unexpected timeout ms: %d", wait.TimeoutMS)
		}

		wait.Ready = 1
		out, err := encodeStruct(wait, containerWaitReadySize)
		if err != nil {
			t.Fatalf("encode wait-ready response: %v", err)
		}
		copy(buf, out)
		return nil
	}

	if err := transport.WaitReady(context.Background(), 7, "kernel-a", 2500*time.Millisecond); err != nil {
		t.Fatalf("WaitReady returned error: %v", err)
	}
	if !file.closed {
		t.Fatalf("device file was not closed")
	}
}

func TestDeviceTransportForcePeerReady(t *testing.T) {
	file := &fakeDeviceFile{fd: 42}
	transport := NewDeviceTransport("/dev/mkring_container_bridge")
	transport.open = func(path string) (deviceFile, error) {
		if path != "/dev/mkring_container_bridge" {
			t.Fatalf("unexpected device path: %s", path)
		}
		return file, nil
	}
	transport.ioctl = func(fd uintptr, req uintptr, arg uintptr) error {
		if fd != file.fd {
			t.Fatalf("unexpected fd: %d", fd)
		}
		if req != ioctlForcePeerReady {
			t.Fatalf("unexpected ioctl request: %#x", req)
		}

		buf := argBytes(arg, containerForcePeerReadySize)
		var mark containerForcePeerReady
		if err := decodeStruct(buf, &mark); err != nil {
			t.Fatalf("decode force-peer-ready request: %v", err)
		}
		if mark.PeerKernelID != 7 {
			t.Fatalf("unexpected peer kernel id: %d", mark.PeerKernelID)
		}
		return nil
	}

	if err := transport.ForcePeerReady(context.Background(), 7, "kernel-a"); err != nil {
		t.Fatalf("ForcePeerReady returned error: %v", err)
	}
	if !file.closed {
		t.Fatalf("device file was not closed")
	}
}

func TestDeviceTransportRoundTripCreate(t *testing.T) {
	file := &fakeDeviceFile{fd: 52}
	transport := NewDeviceTransport("/dev/mkring_container_bridge")
	transport.open = func(path string) (deviceFile, error) {
		if path != "/dev/mkring_container_bridge" {
			t.Fatalf("unexpected device path: %s", path)
		}
		return file, nil
	}
	transport.ioctl = func(fd uintptr, req uintptr, arg uintptr) error {
		if fd != file.fd {
			t.Fatalf("unexpected fd: %d", fd)
		}
		if req != ioctlCall {
			t.Fatalf("unexpected ioctl request: %#x", req)
		}

		buf := argBytes(arg, containerCallSize)
		var call containerCall
		if err := decodeStruct(buf, &call); err != nil {
			t.Fatalf("decode ioctl call: %v", err)
		}

		if call.PeerKernelID != 9 {
			t.Fatalf("unexpected peer kernel id: %d", call.PeerKernelID)
		}
		if call.Request.Header.Magic != mkringContainerMagic {
			t.Fatalf("unexpected request magic: %#x", call.Request.Header.Magic)
		}
		if call.Request.Header.Kind != mkringContainerKindRequest {
			t.Fatalf("unexpected request kind: %d", call.Request.Header.Kind)
		}
		if call.Request.Header.Operation != mkringContainerOpCreate {
			t.Fatalf("unexpected request operation: %d", call.Request.Header.Operation)
		}
		if call.Request.Header.PayloadLen != containerCreateRequestSize {
			t.Fatalf("unexpected request payload length: %d", call.Request.Header.PayloadLen)
		}

		var createReq containerCreateRequest
		if err := decodeStruct(call.Request.Payload[:containerCreateRequestSize], &createReq); err != nil {
			t.Fatalf("decode create request: %v", err)
		}
		if got := cString(createReq.KernelID[:]); got != "kernel-a" {
			t.Fatalf("unexpected kernel id: %q", got)
		}
		if got := cString(createReq.PodID[:]); got != "pod-a" {
			t.Fatalf("unexpected pod id: %q", got)
		}
		if got := cString(createReq.Name[:]); got != "ctr-a" {
			t.Fatalf("unexpected container name: %q", got)
		}
		if got := cString(createReq.Image[:]); got != "busybox:latest" {
			t.Fatalf("unexpected image: %q", got)
		}
		if got := cString(createReq.LogPath[:]); got != "/var/log/ctr-a.log" {
			t.Fatalf("unexpected log path: %q", got)
		}
		if createReq.ArgvCount != 4 {
			t.Fatalf("unexpected argv count: %d", createReq.ArgvCount)
		}
		if got := cString(createReq.Argv[0][:]); got != "sh" {
			t.Fatalf("unexpected argv[0]: %q", got)
		}
		if got := cString(createReq.Argv[1][:]); got != "-c" {
			t.Fatalf("unexpected argv[1]: %q", got)
		}
		if got := cString(createReq.Argv[2][:]); got != "echo tick" {
			t.Fatalf("unexpected argv[2]: %q", got)
		}
		if got := cString(createReq.Argv[3][:]); got != "--flag" {
			t.Fatalf("unexpected argv[3]: %q", got)
		}

		createResp := containerCreateResponse{}
		if err := copyCString(createResp.ContainerID[:], "container-123"); err != nil {
			t.Fatalf("copy container id: %v", err)
		}
		if err := copyCString(createResp.ImageRef[:], "sha256:deadbeef"); err != nil {
			t.Fatalf("copy image ref: %v", err)
		}
		payload, err := encodeStruct(createResp, containerCreateResponseSize)
		if err != nil {
			t.Fatalf("encode create response: %v", err)
		}

		call.Status = 0
		call.Response.Header = containerHeader{
			Magic:      mkringContainerMagic,
			Version:    mkringContainerVersion,
			Channel:    mkringContainerChannel,
			Kind:       mkringContainerKindResponse,
			Operation:  mkringContainerOpCreate,
			PayloadLen: containerCreateResponseSize,
		}
		copy(call.Response.Payload[:], payload)

		out, err := encodeStruct(call, containerCallSize)
		if err != nil {
			t.Fatalf("encode ioctl response: %v", err)
		}
		copy(buf, out)
		return nil
	}

	req, err := NewRequest("req-1", 9, "kernel-a", OpCreateContainer, CreateContainerPayload{
		KernelID: "kernel-a",
		PodID:    "pod-a",
		Name:     "ctr-a",
		Image:    "busybox:latest",
		Command:  []string{"sh", "-c", "echo tick"},
		Args:     []string{"--flag"},
		LogPath:  "/var/log/ctr-a.log",
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := transport.RoundTrip(context.Background(), 9, req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected response error: %+v", resp.Error)
	}
	if resp.ID != req.ID {
		t.Fatalf("unexpected response id: got=%s want=%s", resp.ID, req.ID)
	}

	var result CreateContainerResult
	if err := DecodePayload(resp, &result); err != nil {
		t.Fatalf("decode response payload: %v", err)
	}
	if result.ContainerID != "container-123" {
		t.Fatalf("unexpected container id: %q", result.ContainerID)
	}
	if result.ImageRef != "sha256:deadbeef" {
		t.Fatalf("unexpected image ref: %q", result.ImageRef)
	}
	if !file.closed {
		t.Fatalf("device file was not closed")
	}
}

func TestDeviceTransportRoundTripExecTTYPrepare(t *testing.T) {
	file := &fakeDeviceFile{fd: 63}
	transport := NewDeviceTransport("/dev/mkring_container_bridge")
	transport.open = func(path string) (deviceFile, error) {
		if path != "/dev/mkring_container_bridge" {
			t.Fatalf("unexpected device path: %s", path)
		}
		return file, nil
	}
	transport.ioctl = func(fd uintptr, req uintptr, arg uintptr) error {
		if fd != file.fd {
			t.Fatalf("unexpected fd: %d", fd)
		}
		if req != ioctlCall {
			t.Fatalf("unexpected ioctl request: %#x", req)
		}

		buf := argBytes(arg, containerCallSize)
		var call containerCall
		if err := decodeStruct(buf, &call); err != nil {
			t.Fatalf("decode ioctl call: %v", err)
		}

		if call.Request.Header.Operation != mkringContainerOpExecTTYPrepare {
			t.Fatalf("unexpected request operation: %d", call.Request.Header.Operation)
		}
		if call.Request.Header.PayloadLen != containerExecTTYPrepareRequestSize {
			t.Fatalf("unexpected request payload length: %d", call.Request.Header.PayloadLen)
		}

		var execReq containerExecTTYPrepareRequest
		if err := decodeStruct(call.Request.Payload[:containerExecTTYPrepareRequestSize], &execReq); err != nil {
			t.Fatalf("decode exec-tty-prepare request: %v", err)
		}
		if got := cString(execReq.KernelID[:]); got != "kernel-a" {
			t.Fatalf("unexpected kernel id: %q", got)
		}
		if got := cString(execReq.ContainerID[:]); got != "ctr-a" {
			t.Fatalf("unexpected container id: %q", got)
		}
		if execReq.ArgvCount != 2 {
			t.Fatalf("unexpected argv count: %d", execReq.ArgvCount)
		}
		if execReq.TTY != 1 || execReq.StdinEnabled != 1 || execReq.StdoutEnabled != 1 {
			t.Fatalf("unexpected tty stream flags: tty=%d stdin=%d stdout=%d",
				execReq.TTY, execReq.StdinEnabled, execReq.StdoutEnabled)
		}
		if execReq.StderrEnabled != 0 {
			t.Fatalf("unexpected stderr flag: %d", execReq.StderrEnabled)
		}
		if got := cString(execReq.Argv[0][:]); got != "sh" {
			t.Fatalf("unexpected argv[0]: %q", got)
		}
		if got := cString(execReq.Argv[1][:]); got != "-l" {
			t.Fatalf("unexpected argv[1]: %q", got)
		}

		execResp := containerExecTTYPrepareResponse{}
		if err := copyCString(execResp.SessionID[:], "exec-session-1"); err != nil {
			t.Fatalf("copy session id: %v", err)
		}
		payload, err := encodeStruct(execResp, containerExecTTYPrepareResponseSize)
		if err != nil {
			t.Fatalf("encode exec-tty-prepare response: %v", err)
		}

		call.Status = 0
		call.Response.Header = containerHeader{
			Magic:      mkringContainerMagic,
			Version:    mkringContainerVersion,
			Channel:    mkringContainerChannel,
			Kind:       mkringContainerKindResponse,
			Operation:  mkringContainerOpExecTTYPrepare,
			PayloadLen: containerExecTTYPrepareResponseSize,
		}
		copy(call.Response.Payload[:], payload)

		out, err := encodeStruct(call, containerCallSize)
		if err != nil {
			t.Fatalf("encode ioctl response: %v", err)
		}
		copy(buf, out)
		return nil
	}

	req, err := NewRequest("req-exec-1", 9, "kernel-a", OpExecTTYPrepare, ExecTTYPreparePayload{
		KernelID:    "kernel-a",
		ContainerID: "ctr-a",
		Command:     []string{"sh", "-l"},
		TTY:         true,
		Stdin:       true,
		Stdout:      true,
		Stderr:      false,
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := transport.RoundTrip(context.Background(), 9, req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected response error: %+v", resp.Error)
	}

	var result ExecTTYPrepareResult
	if err := DecodePayload(resp, &result); err != nil {
		t.Fatalf("decode response payload: %v", err)
	}
	if result.SessionID != "exec-session-1" {
		t.Fatalf("unexpected session id: %q", result.SessionID)
	}
	if !file.closed {
		t.Fatalf("device file was not closed")
	}
}

func TestDeviceTransportRoundTripExecTTYStart(t *testing.T) {
	file := &fakeDeviceFile{fd: 64}
	transport := NewDeviceTransport("/dev/mkring_container_bridge")
	transport.open = func(path string) (deviceFile, error) {
		if path != "/dev/mkring_container_bridge" {
			t.Fatalf("unexpected device path: %s", path)
		}
		return file, nil
	}
	transport.ioctl = func(fd uintptr, req uintptr, arg uintptr) error {
		if fd != file.fd {
			t.Fatalf("unexpected fd: %d", fd)
		}
		if req != ioctlCall {
			t.Fatalf("unexpected ioctl request: %#x", req)
		}

		buf := argBytes(arg, containerCallSize)
		var call containerCall
		if err := decodeStruct(buf, &call); err != nil {
			t.Fatalf("decode ioctl call: %v", err)
		}

		if call.Request.Header.Operation != mkringContainerOpExecTTYStart {
			t.Fatalf("unexpected request operation: %d", call.Request.Header.Operation)
		}
		if call.Request.Header.PayloadLen != containerExecTTYStartRequestSize {
			t.Fatalf("unexpected request payload length: %d", call.Request.Header.PayloadLen)
		}

		var startReq containerExecTTYStartRequest
		if err := decodeStruct(call.Request.Payload[:containerExecTTYStartRequestSize], &startReq); err != nil {
			t.Fatalf("decode exec-tty-start request: %v", err)
		}
		if got := cString(startReq.KernelID[:]); got != "kernel-a" {
			t.Fatalf("unexpected kernel id: %q", got)
		}
		if got := cString(startReq.SessionID[:]); got != "exec-session-1" {
			t.Fatalf("unexpected session id: %q", got)
		}

		call.Status = 0
		call.Response.Header = containerHeader{
			Magic:      mkringContainerMagic,
			Version:    mkringContainerVersion,
			Channel:    mkringContainerChannel,
			Kind:       mkringContainerKindResponse,
			Operation:  mkringContainerOpExecTTYStart,
			PayloadLen: 0,
		}

		out, err := encodeStruct(call, containerCallSize)
		if err != nil {
			t.Fatalf("encode ioctl response: %v", err)
		}
		copy(buf, out)
		return nil
	}

	req, err := NewRequest("req-exec-start-1", 9, "kernel-a", OpExecTTYStart, ExecTTYStartPayload{
		KernelID:  "kernel-a",
		SessionID: "exec-session-1",
	})
	if err != nil {
		t.Fatalf("build request: %v", err)
	}

	resp, err := transport.RoundTrip(context.Background(), 9, req)
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected response error: %+v", resp.Error)
	}
	if !file.closed {
		t.Fatalf("device file was not closed")
	}
}
