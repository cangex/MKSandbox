package mkringcontrol

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	mkringContainerMagic   = 0x4d4b434e
	mkringContainerVersion = 1
	mkringContainerChannel = 1

	mkringContainerKindRequest  = 2
	mkringContainerKindResponse = 3

	mkringContainerOpNone           = 0
	mkringContainerOpCreate         = 1
	mkringContainerOpStart          = 2
	mkringContainerOpStop           = 3
	mkringContainerOpRemove         = 4
	mkringContainerOpStatus         = 5
	mkringContainerOpReadLog        = 6
	mkringContainerOpExecTTYPrepare = 7
	mkringContainerOpExecTTYStart   = 8
	mkringContainerOpExecTTYResize  = 9
	mkringContainerOpExecTTYClose   = 10

	mkringContainerMaxRuntimeName = 16
	mkringContainerMaxKernelID    = 64
	mkringContainerMaxPodID       = 64
	mkringContainerMaxName        = 64
	mkringContainerMaxImage       = 256
	mkringContainerMaxLogPath     = 256
	mkringContainerMaxArgv        = 4
	mkringContainerMaxArgLen      = 64
	mkringContainerMaxContainerID = 64
	mkringContainerMaxImageRef    = 256
	mkringContainerMaxErrorMsg    = 128
	mkringContainerMaxLogChunk    = 384

	containerHeaderSize                 = 28
	containerPayloadSize                = 968
	containerMessageSize                = 996
	containerWaitReadySize              = 12
	containerForcePeerReadySize         = 4
	containerCallSize                   = 2004
	containerCreateRequestSize          = 968
	containerControlRequestSize         = 136
	containerReadLogRequestSize         = 140
	containerExecTTYPrepareRequestSize  = 392
	containerExecTTYStartRequestSize    = 128
	containerExecTTYResizeRequestSize   = 136
	containerExecTTYCloseRequestSize    = 128
	containerCreateResponseSize         = 320
	containerStopResponseSize           = 4
	containerStatusResponseSize         = 156
	containerReadLogResponseSize        = 400
	containerExecTTYPrepareResponseSize = 64
	containerErrorPayloadSize           = 132

	iocNRBits   = 8
	iocTypeBits = 8
	iocSizeBits = 14
	iocDirBits  = 2

	iocNRShift   = 0
	iocTypeShift = iocNRShift + iocNRBits
	iocSizeShift = iocTypeShift + iocTypeBits
	iocDirShift  = iocSizeShift + iocSizeBits

	iocWrite = 1
	iocRead  = 2

	mkringContainerIOCMagic = 0xB7

	ioctlWaitReady = uintptr(((iocRead | iocWrite) << iocDirShift) |
		(mkringContainerIOCMagic << iocTypeShift) |
		(0x01 << iocNRShift) |
		(containerWaitReadySize << iocSizeShift))
	ioctlForcePeerReady = uintptr((iocWrite << iocDirShift) |
		(mkringContainerIOCMagic << iocTypeShift) |
		(0x04 << iocNRShift) |
		(containerForcePeerReadySize << iocSizeShift))
	ioctlCall = uintptr(((iocRead | iocWrite) << iocDirShift) |
		(mkringContainerIOCMagic << iocTypeShift) |
		(0x03 << iocNRShift) |
		(containerCallSize << iocSizeShift))
)

type deviceFile interface {
	Fd() uintptr
	Close() error
}

type fileOpener func(path string) (deviceFile, error)
type ioctlFunc func(fd uintptr, req uintptr, arg uintptr) error

type DeviceTransport struct {
	devicePath string
	open       fileOpener
	ioctl      ioctlFunc
}

type containerHeader struct {
	Magic      uint32
	Version    uint8
	Channel    uint8
	Kind       uint8
	Operation  uint8
	Flags      uint16
	Reserved0  uint16
	RequestID  uint64
	Status     int32
	PayloadLen uint32
}

type containerMessage struct {
	Header  containerHeader
	Payload [containerPayloadSize]byte
}

type containerWaitReady struct {
	PeerKernelID uint16
	Reserved0    uint16
	TimeoutMS    uint32
	Ready        uint32
}

type containerForcePeerReady struct {
	PeerKernelID uint16
	Reserved0    uint16
}

type containerCall struct {
	PeerKernelID uint16
	Reserved0    uint16
	TimeoutMS    uint32
	Status       int32
	Request      containerMessage
	Response     containerMessage
}

type containerCreateRequest struct {
	KernelID  [mkringContainerMaxKernelID]byte
	PodID     [mkringContainerMaxPodID]byte
	Name      [mkringContainerMaxName]byte
	Image     [mkringContainerMaxImage]byte
	LogPath   [mkringContainerMaxLogPath]byte
	ArgvCount uint32
	Reserved0 uint32
	Argv      [mkringContainerMaxArgv][mkringContainerMaxArgLen]byte
}

type containerControlRequest struct {
	KernelID      [mkringContainerMaxKernelID]byte
	ContainerID   [mkringContainerMaxContainerID]byte
	TimeoutMillis int64
}

type containerReadLogRequest struct {
	KernelID    [mkringContainerMaxKernelID]byte
	ContainerID [mkringContainerMaxContainerID]byte
	Offset      uint64
	MaxBytes    uint32
}

type containerExecTTYPrepareRequest struct {
	KernelID      [mkringContainerMaxKernelID]byte
	ContainerID   [mkringContainerMaxContainerID]byte
	ArgvCount     uint32
	TTY           uint8
	StdinEnabled  uint8
	StdoutEnabled uint8
	StderrEnabled uint8
	Argv          [mkringContainerMaxArgv][mkringContainerMaxArgLen]byte
}

type containerExecTTYStartRequest struct {
	KernelID  [mkringContainerMaxKernelID]byte
	SessionID [mkringContainerMaxContainerID]byte
}

type containerExecTTYResizeRequest struct {
	KernelID  [mkringContainerMaxKernelID]byte
	SessionID [mkringContainerMaxContainerID]byte
	Width     uint32
	Height    uint32
}

type containerExecTTYCloseRequest struct {
	KernelID  [mkringContainerMaxKernelID]byte
	SessionID [mkringContainerMaxContainerID]byte
}

type containerCreateResponse struct {
	ContainerID [mkringContainerMaxContainerID]byte
	ImageRef    [mkringContainerMaxImageRef]byte
}

type containerStopResponse struct {
	ExitCode int32
}

type containerStatusResponse struct {
	State              uint32
	ExitCode           int32
	PID                int32
	StartedAtUnixNano  uint64
	FinishedAtUnixNano uint64
	Message            [mkringContainerMaxErrorMsg]byte
}

type containerReadLogResponse struct {
	NextOffset uint64
	DataLen    uint32
	EOF        uint8
	Reserved0  [3]byte
	Data       [mkringContainerMaxLogChunk]byte
}

type containerExecTTYPrepareResponse struct {
	SessionID [mkringContainerMaxContainerID]byte
}

type containerErrorPayload struct {
	ErrnoValue int32
	Message    [mkringContainerMaxErrorMsg]byte
}

func NewDeviceTransport(devicePath string) *DeviceTransport {
	return &DeviceTransport{
		devicePath: devicePath,
		open: func(path string) (deviceFile, error) {
			return os.OpenFile(path, os.O_RDWR, 0)
		},
		ioctl: func(fd uintptr, req uintptr, arg uintptr) error {
			_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
			if errno != 0 {
				return errno
			}
			return nil
		},
	}
}

func (t *DeviceTransport) WaitReady(ctx context.Context, peerKernelID uint16, kernelID string, timeout time.Duration) error {
	timeoutMS, err := effectiveTimeoutMillis(ctx, timeout)
	if err != nil {
		return err
	}

	arg := containerWaitReady{
		PeerKernelID: peerKernelID,
		TimeoutMS:    timeoutMS,
	}
	buf, err := encodeStruct(arg, containerWaitReadySize)
	if err != nil {
		return err
	}

	if err := t.ioctlOnDevice(ioctlWaitReady, buf); err != nil {
		return fmt.Errorf("wait ready peer=%d kernel=%s via %s: %w", peerKernelID, kernelID, t.devicePath, err)
	}

	var result containerWaitReady
	if err := decodeStruct(buf, &result); err != nil {
		return err
	}
	if result.Ready == 0 {
		return fmt.Errorf("wait ready peer=%d kernel=%s via %s returned not ready", peerKernelID, kernelID, t.devicePath)
	}
	return nil
}

func (t *DeviceTransport) ForcePeerReady(ctx context.Context, peerKernelID uint16, kernelID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	arg := containerForcePeerReady{
		PeerKernelID: peerKernelID,
	}
	buf, err := encodeStruct(arg, containerForcePeerReadySize)
	if err != nil {
		return err
	}

	if err := t.ioctlOnDevice(ioctlForcePeerReady, buf); err != nil {
		return fmt.Errorf("force peer ready peer=%d kernel=%s via %s: %w", peerKernelID, kernelID, t.devicePath, err)
	}

	return nil
}

func (t *DeviceTransport) RoundTrip(ctx context.Context, peerKernelID uint16, req Envelope) (Envelope, error) {
	call, err := t.encodeCall(ctx, peerKernelID, req)
	if err != nil {
		return Envelope{}, err
	}

	buf, err := encodeStruct(call, containerCallSize)
	if err != nil {
		return Envelope{}, err
	}

	if err := t.ioctlOnDevice(ioctlCall, buf); err != nil {
		return Envelope{}, fmt.Errorf("call peer=%d op=%s via %s: %w", peerKernelID, req.Operation, t.devicePath, err)
	}

	var result containerCall
	if err := decodeStruct(buf, &result); err != nil {
		return Envelope{}, err
	}

	return decodeResponseEnvelope(req, result)
}

func (t *DeviceTransport) encodeCall(ctx context.Context, peerKernelID uint16, req Envelope) (containerCall, error) {
	if req.Kind != MessageKindRequest {
		return containerCall{}, fmt.Errorf("device transport expects request envelope, got %q", req.Kind)
	}

	timeoutMS, err := timeoutMillisForRequest(ctx, req)
	if err != nil {
		return containerCall{}, err
	}

	msg, err := encodeRequestMessage(req)
	if err != nil {
		return containerCall{}, err
	}

	return containerCall{
		PeerKernelID: peerKernelID,
		TimeoutMS:    timeoutMS,
		Request:      msg,
	}, nil
}

func (t *DeviceTransport) ioctlOnDevice(req uintptr, arg []byte) error {
	if len(arg) == 0 {
		return errors.New("empty ioctl buffer")
	}

	file, err := t.open(t.devicePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", t.devicePath, err)
	}
	defer file.Close()

	err = t.ioctl(file.Fd(), req, uintptr(unsafe.Pointer(&arg[0])))
	runtime.KeepAlive(arg)
	runtime.KeepAlive(file)
	return err
}

func effectiveTimeoutMillis(ctx context.Context, timeout time.Duration) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	effective := timeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, context.DeadlineExceeded
		}
		if effective <= 0 || remaining < effective {
			effective = remaining
		}
	}

	if effective <= 0 {
		return 0, nil
	}

	millis := effective.Milliseconds()
	if millis <= 0 {
		millis = 1
	}
	if millis > math.MaxUint32 {
		millis = math.MaxUint32
	}
	return uint32(millis), nil
}

func timeoutMillisForRequest(ctx context.Context, req Envelope) (uint32, error) {
	switch req.Operation {
	case OpCreateContainer:
		return effectiveTimeoutMillis(ctx, 0)
	case OpStartContainer, OpStopContainer, OpRemoveContainer, OpStatusContainer:
		var payload ContainerControlPayload
		if err := DecodePayload(req, &payload); err != nil {
			return 0, err
		}
		return effectiveTimeoutMillis(ctx, time.Duration(payload.TimeoutMillis)*time.Millisecond)
	case OpReadLog:
		return effectiveTimeoutMillis(ctx, 0)
	case OpExecTTYPrepare, OpExecTTYStart, OpExecTTYResize, OpExecTTYClose:
		return effectiveTimeoutMillis(ctx, 0)
	default:
		return 0, fmt.Errorf("unsupported operation %q", req.Operation)
	}

	return 0, fmt.Errorf("unsupported operation %q", req.Operation)
}

func encodeRequestMessage(req Envelope) (containerMessage, error) {
	op, err := operationToUAPI(req.Operation)
	if err != nil {
		return containerMessage{}, err
	}

	msg := containerMessage{}
	msg.Header = containerHeader{
		Magic:     mkringContainerMagic,
		Version:   mkringContainerVersion,
		Channel:   mkringContainerChannel,
		Kind:      mkringContainerKindRequest,
		Operation: op,
	}

	switch req.Operation {
	case OpCreateContainer:
		var payload CreateContainerPayload
		if err := DecodePayload(req, &payload); err != nil {
			return containerMessage{}, err
		}
		argv := flattenArgv(payload.Command, payload.Args)
		if len(argv) > mkringContainerMaxArgv {
			return containerMessage{}, fmt.Errorf("argv length %d exceeds max %d", len(argv), mkringContainerMaxArgv)
		}
		create := containerCreateRequest{}
		if err := copyCString(create.KernelID[:], payload.KernelID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(create.PodID[:], payload.PodID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(create.Name[:], payload.Name); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(create.Image[:], payload.Image); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(create.LogPath[:], payload.LogPath); err != nil {
			return containerMessage{}, err
		}
		create.ArgvCount = uint32(len(argv))
		for i, arg := range argv {
			if err := copyCString(create.Argv[i][:], arg); err != nil {
				return containerMessage{}, err
			}
		}
		encoded, err := encodeStruct(create, containerCreateRequestSize)
		if err != nil {
			return containerMessage{}, err
		}
		copy(msg.Payload[:], encoded)
		msg.Header.PayloadLen = uint32(containerCreateRequestSize)
	case OpStartContainer, OpStopContainer, OpRemoveContainer, OpStatusContainer:
		var payload ContainerControlPayload
		if err := DecodePayload(req, &payload); err != nil {
			return containerMessage{}, err
		}
		control := containerControlRequest{TimeoutMillis: payload.TimeoutMillis}
		if err := copyCString(control.KernelID[:], payload.KernelID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(control.ContainerID[:], payload.ContainerID); err != nil {
			return containerMessage{}, err
		}
		encoded, err := encodeStruct(control, containerControlRequestSize)
		if err != nil {
			return containerMessage{}, err
		}
		copy(msg.Payload[:], encoded)
		msg.Header.PayloadLen = uint32(containerControlRequestSize)
	case OpReadLog:
		var payload ReadLogPayload
		if err := DecodePayload(req, &payload); err != nil {
			return containerMessage{}, err
		}
		readLog := containerReadLogRequest{
			Offset:   payload.Offset,
			MaxBytes: payload.MaxBytes,
		}
		if err := copyCString(readLog.KernelID[:], payload.KernelID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(readLog.ContainerID[:], payload.ContainerID); err != nil {
			return containerMessage{}, err
		}
		encoded, err := encodeStruct(readLog, containerReadLogRequestSize)
		if err != nil {
			return containerMessage{}, err
		}
		copy(msg.Payload[:], encoded)
		msg.Header.PayloadLen = uint32(containerReadLogRequestSize)
	case OpExecTTYPrepare:
		var payload ExecTTYPreparePayload
		if err := DecodePayload(req, &payload); err != nil {
			return containerMessage{}, err
		}
		if len(payload.Command) > mkringContainerMaxArgv {
			return containerMessage{}, fmt.Errorf("argv length %d exceeds max %d", len(payload.Command), mkringContainerMaxArgv)
		}
		execReq := containerExecTTYPrepareRequest{
			TTY:           boolToU8(payload.TTY),
			StdinEnabled:  boolToU8(payload.Stdin),
			StdoutEnabled: boolToU8(payload.Stdout),
			StderrEnabled: boolToU8(payload.Stderr),
			ArgvCount:     uint32(len(payload.Command)),
		}
		if err := copyCString(execReq.KernelID[:], payload.KernelID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(execReq.ContainerID[:], payload.ContainerID); err != nil {
			return containerMessage{}, err
		}
		for i, arg := range payload.Command {
			if err := copyCString(execReq.Argv[i][:], arg); err != nil {
				return containerMessage{}, err
			}
		}
		encoded, err := encodeStruct(execReq, containerExecTTYPrepareRequestSize)
		if err != nil {
			return containerMessage{}, err
		}
		copy(msg.Payload[:], encoded)
		msg.Header.PayloadLen = uint32(containerExecTTYPrepareRequestSize)
	case OpExecTTYStart:
		var payload ExecTTYStartPayload
		if err := DecodePayload(req, &payload); err != nil {
			return containerMessage{}, err
		}
		startReq := containerExecTTYStartRequest{}
		if err := copyCString(startReq.KernelID[:], payload.KernelID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(startReq.SessionID[:], payload.SessionID); err != nil {
			return containerMessage{}, err
		}
		encoded, err := encodeStruct(startReq, containerExecTTYStartRequestSize)
		if err != nil {
			return containerMessage{}, err
		}
		copy(msg.Payload[:], encoded)
		msg.Header.PayloadLen = uint32(containerExecTTYStartRequestSize)
	case OpExecTTYResize:
		var payload ExecTTYResizePayload
		if err := DecodePayload(req, &payload); err != nil {
			return containerMessage{}, err
		}
		resizeReq := containerExecTTYResizeRequest{
			Width:  payload.Width,
			Height: payload.Height,
		}
		if err := copyCString(resizeReq.KernelID[:], payload.KernelID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(resizeReq.SessionID[:], payload.SessionID); err != nil {
			return containerMessage{}, err
		}
		encoded, err := encodeStruct(resizeReq, containerExecTTYResizeRequestSize)
		if err != nil {
			return containerMessage{}, err
		}
		copy(msg.Payload[:], encoded)
		msg.Header.PayloadLen = uint32(containerExecTTYResizeRequestSize)
	case OpExecTTYClose:
		var payload ExecTTYClosePayload
		if err := DecodePayload(req, &payload); err != nil {
			return containerMessage{}, err
		}
		closeReq := containerExecTTYCloseRequest{}
		if err := copyCString(closeReq.KernelID[:], payload.KernelID); err != nil {
			return containerMessage{}, err
		}
		if err := copyCString(closeReq.SessionID[:], payload.SessionID); err != nil {
			return containerMessage{}, err
		}
		encoded, err := encodeStruct(closeReq, containerExecTTYCloseRequestSize)
		if err != nil {
			return containerMessage{}, err
		}
		copy(msg.Payload[:], encoded)
		msg.Header.PayloadLen = uint32(containerExecTTYCloseRequestSize)
	default:
		return containerMessage{}, fmt.Errorf("unsupported operation %q", req.Operation)
	}

	return msg, nil
}

func flattenArgv(command []string, args []string) []string {
	if len(command) == 0 && len(args) == 0 {
		return nil
	}

	argv := make([]string, 0, len(command)+len(args))
	argv = append(argv, command...)
	argv = append(argv, args...)
	return argv
}

func decodeResponseEnvelope(req Envelope, call containerCall) (Envelope, error) {
	resp := call.Response
	if resp.Header.Magic != mkringContainerMagic {
		return Envelope{}, fmt.Errorf("unexpected response magic 0x%x", resp.Header.Magic)
	}
	if resp.Header.Version != mkringContainerVersion {
		return Envelope{}, fmt.Errorf("unexpected response version %d", resp.Header.Version)
	}
	if resp.Header.Channel != mkringContainerChannel {
		return Envelope{}, fmt.Errorf("unexpected response channel %d", resp.Header.Channel)
	}
	if resp.Header.Kind != mkringContainerKindResponse {
		return Envelope{}, fmt.Errorf("unexpected response kind %d", resp.Header.Kind)
	}
	if resp.Header.Operation != call.Request.Header.Operation {
		return Envelope{}, fmt.Errorf("unexpected response operation %d", resp.Header.Operation)
	}
	if resp.Header.PayloadLen > containerPayloadSize {
		return Envelope{}, fmt.Errorf("response payload too large: %d", resp.Header.PayloadLen)
	}

	status := resp.Header.Status
	if call.Status != 0 {
		status = call.Status
	}
	if status != 0 {
		return decodeErrorEnvelope(req, resp, status), nil
	}

	switch req.Operation {
	case OpCreateContainer:
		if resp.Header.PayloadLen != containerCreateResponseSize {
			return Envelope{}, fmt.Errorf("unexpected create response payload length %d", resp.Header.PayloadLen)
		}
		var payload containerCreateResponse
		if err := decodeStruct(resp.Payload[:containerCreateResponseSize], &payload); err != nil {
			return Envelope{}, err
		}
		return NewResponse(req, CreateContainerResult{
			ContainerID: cString(payload.ContainerID[:]),
			ImageRef:    cString(payload.ImageRef[:]),
		})
	case OpStartContainer, OpRemoveContainer, OpExecTTYStart, OpExecTTYResize, OpExecTTYClose:
		if resp.Header.PayloadLen != 0 {
			return Envelope{}, fmt.Errorf("unexpected empty-response payload length %d", resp.Header.PayloadLen)
		}
		return NewResponse(req, struct{}{})
	case OpStopContainer:
		if resp.Header.PayloadLen != containerStopResponseSize {
			return Envelope{}, fmt.Errorf("unexpected stop response payload length %d", resp.Header.PayloadLen)
		}
		var payload containerStopResponse
		if err := decodeStruct(resp.Payload[:containerStopResponseSize], &payload); err != nil {
			return Envelope{}, err
		}
		return NewResponse(req, StopContainerResult{ExitCode: payload.ExitCode})
	case OpStatusContainer:
		if resp.Header.PayloadLen != containerStatusResponseSize {
			return Envelope{}, fmt.Errorf("unexpected status response payload length %d", resp.Header.PayloadLen)
		}
		var payload containerStatusResponse
		if err := decodeStruct(resp.Payload[:containerStatusResponseSize], &payload); err != nil {
			return Envelope{}, err
		}
		return NewResponse(req, ContainerStatusResult{
			State:              uapiStateToProtocol(payload.State),
			ExitCode:           payload.ExitCode,
			PID:                payload.PID,
			StartedAtUnixNano:  payload.StartedAtUnixNano,
			FinishedAtUnixNano: payload.FinishedAtUnixNano,
			Message:            cString(payload.Message[:]),
		})
	case OpReadLog:
		if resp.Header.PayloadLen != containerReadLogResponseSize {
			return Envelope{}, fmt.Errorf("unexpected read-log response payload length %d", resp.Header.PayloadLen)
		}
		var payload containerReadLogResponse
		if err := decodeStruct(resp.Payload[:containerReadLogResponseSize], &payload); err != nil {
			return Envelope{}, err
		}
		if payload.DataLen > mkringContainerMaxLogChunk {
			return Envelope{}, fmt.Errorf("read-log response data too large: %d", payload.DataLen)
		}
		data := make([]byte, payload.DataLen)
		copy(data, payload.Data[:payload.DataLen])
		return NewResponse(req, ReadLogResult{
			NextOffset: payload.NextOffset,
			EOF:        payload.EOF != 0,
			Data:       data,
		})
	case OpExecTTYPrepare:
		if resp.Header.PayloadLen != containerExecTTYPrepareResponseSize {
			return Envelope{}, fmt.Errorf("unexpected exec-tty-prepare response payload length %d", resp.Header.PayloadLen)
		}
		var payload containerExecTTYPrepareResponse
		if err := decodeStruct(resp.Payload[:containerExecTTYPrepareResponseSize], &payload); err != nil {
			return Envelope{}, err
		}
		return NewResponse(req, ExecTTYPrepareResult{
			SessionID: cString(payload.SessionID[:]),
		})
	default:
		return Envelope{}, fmt.Errorf("unsupported operation %q", req.Operation)
	}

	return Envelope{}, fmt.Errorf("unsupported operation %q", req.Operation)
}

func decodeErrorEnvelope(req Envelope, resp containerMessage, status int32) Envelope {
	message := fmt.Sprintf("remote error: status=%d", status)
	code := statusCodeString(status)

	if resp.Header.PayloadLen == containerErrorPayloadSize {
		var payload containerErrorPayload
		if err := decodeStruct(resp.Payload[:containerErrorPayloadSize], &payload); err == nil {
			if payload.ErrnoValue != 0 {
				code = statusCodeString(payload.ErrnoValue)
			}
			if text := cString(payload.Message[:]); text != "" {
				message = text
			}
		}
	}

	return NewErrorResponse(req, code, message)
}

func operationToUAPI(op Operation) (uint8, error) {
	switch op {
	case OpCreateContainer:
		return mkringContainerOpCreate, nil
	case OpStartContainer:
		return mkringContainerOpStart, nil
	case OpStopContainer:
		return mkringContainerOpStop, nil
	case OpRemoveContainer:
		return mkringContainerOpRemove, nil
	case OpStatusContainer:
		return mkringContainerOpStatus, nil
	case OpReadLog:
		return mkringContainerOpReadLog, nil
	case OpExecTTYPrepare:
		return mkringContainerOpExecTTYPrepare, nil
	case OpExecTTYStart:
		return mkringContainerOpExecTTYStart, nil
	case OpExecTTYResize:
		return mkringContainerOpExecTTYResize, nil
	case OpExecTTYClose:
		return mkringContainerOpExecTTYClose, nil
	default:
		return 0, fmt.Errorf("unsupported operation %q", op)
	}
}

func boolToU8(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}

func uapiStateToProtocol(state uint32) ContainerStatusState {
	switch state {
	case 1:
		return ContainerStatusCreated
	case 2:
		return ContainerStatusRunning
	case 3:
		return ContainerStatusExited
	default:
		return ContainerStatusUnknown
	}
}

func statusCodeString(status int32) string {
	if status < 0 {
		return fmt.Sprintf("errno_%d", -status)
	}
	return fmt.Sprintf("status_%d", status)
}

func cString(buf []byte) string {
	if i := bytes.IndexByte(buf, 0); i >= 0 {
		buf = buf[:i]
	}
	return string(buf)
}

func copyCString(dst []byte, value string) error {
	if len(value) > len(dst) {
		return fmt.Errorf("field too long: got=%d max=%d", len(value), len(dst))
	}
	copy(dst, value)
	return nil
}

func encodeStruct(v interface{}, expectedSize int) ([]byte, error) {
	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
		return nil, err
	}
	if expectedSize > 0 && buf.Len() != expectedSize {
		return nil, fmt.Errorf("unexpected struct size: got=%d want=%d", buf.Len(), expectedSize)
	}
	return buf.Bytes(), nil
}

func decodeStruct(data []byte, out interface{}) error {
	return binary.Read(bytes.NewReader(data), binary.LittleEndian, out)
}
