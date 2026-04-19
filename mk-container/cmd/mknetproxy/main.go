//go:build linux
// +build linux

package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	mkringTransportOpSend = 1
	mkringTransportOpRecv = 2
	mkringMaxMessage      = 1024

	mknetMagic      = 0x4d4b434e
	mknetVersion    = 1
	mknetChannel    = 4
	mknetMaxPayload = 768

	mknetOpen    = 1
	mknetOpenOK  = 2
	mknetOpenErr = 3
	mknetData    = 4
	mknetClose   = 5
	mknetReset   = 6
)

type transportSend struct {
	PeerKernelID uint16
	Channel      uint16
	MessageLen   uint32
	Message      [mkringMaxMessage]byte
}

type transportRecv struct {
	PeerKernelID uint16
	Channel      uint16
	TimeoutMS    uint32
	MessageLen   uint32
	Message      [mkringMaxMessage]byte
}

type netHeader struct {
	Magic      uint32
	Version    uint8
	Channel    uint8
	MsgType    uint8
	Flags      uint8
	ConnID     uint64
	Seq        uint32
	PayloadLen uint32
	SrcIPBE    uint32
	DstIPBE    uint32
	SrcPortBE  uint16
	DstPortBE  uint16
}

type netMessage struct {
	Header  netHeader
	Payload [mknetMaxPayload]byte
}

type forwardSpec struct {
	Addr         string
	PeerKernelID uint16
}

type forwardList []forwardSpec

func (f *forwardList) String() string {
	var items []string
	for _, item := range *f {
		items = append(items, fmt.Sprintf("%s=%d", item.Addr, item.PeerKernelID))
	}
	return strings.Join(items, ",")
}

func (f *forwardList) Set(value string) error {
	idx := strings.IndexByte(value, '=')
	if idx < 0 {
		return fmt.Errorf("forward must be addr:port=peer_kernel_id")
	}
	addr, peer := value[:idx], value[idx+1:]
	if addr == "" || peer == "" {
		return fmt.Errorf("forward must be addr:port=peer_kernel_id")
	}
	parsed, err := strconv.ParseUint(peer, 10, 16)
	if err != nil {
		return fmt.Errorf("parse peer kernel id: %w", err)
	}
	*f = append(*f, forwardSpec{Addr: addr, PeerKernelID: uint16(parsed)})
	return nil
}

type proxy struct {
	trap       uintptr
	targetHost string
	nextID     uint64

	mu    sync.Mutex
	conns map[uint64]net.Conn
	peers map[uint64]uint16
}

func main() {
	var forwards forwardList
	var syscallNo uint
	var targetHost string
	var recvTimeout uint

	flag.UintVar(&syscallNo, "syscall", 470, "mkring_transport syscall number")
	flag.StringVar(&targetHost, "target-host", "127.0.0.1", "host used by the remote proxy when dialing the local service")
	flag.UintVar(&recvTimeout, "recv-timeout-ms", 1000, "mkring receive timeout")
	flag.Var(&forwards, "forward", "listen_addr:port=peer_kernel_id; repeat for each explicit CloudSuite endpoint")
	flag.Parse()

	p := &proxy{
		trap:       uintptr(syscallNo),
		targetHost: targetHost,
		nextID:     uint64(time.Now().UnixNano()),
		conns:      map[uint64]net.Conn{},
		peers:      map[uint64]uint16{},
	}

	go p.recvLoop(time.Duration(recvTimeout) * time.Millisecond)

	for _, f := range forwards {
		f := f
		go func() {
			if err := p.serveForward(f); err != nil {
				log.Printf("forward %s failed: %v", f.Addr, err)
			}
		}()
	}

	select {}
}

func (p *proxy) serveForward(f forwardSpec) error {
	ln, err := net.Listen("tcp", f.Addr)
	if err != nil {
		return err
	}
	log.Printf("mknet forward listen=%s peer_kernel_id=%d", f.Addr, f.PeerKernelID)
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		connID := atomic.AddUint64(&p.nextID, 1)
		p.addConn(connID, f.PeerKernelID, conn)
		go p.handleLocal(connID, f.PeerKernelID, conn, f.Addr)
	}
}

func (p *proxy) handleLocal(connID uint64, peer uint16, conn net.Conn, dst string) {
	host, portText, err := net.SplitHostPort(dst)
	if err != nil {
		log.Printf("mknet bad destination %s: %v", dst, err)
		_ = conn.Close()
		return
	}
	port, _ := strconv.Atoi(portText)
	dstIP := ipv4ToBE(host)

	if err := p.send(peer, mknetOpen, connID, nil, dstIP, uint16(port)); err != nil {
		log.Printf("mknet open conn=%d peer=%d failed: %v", connID, peer, err)
		_ = conn.Close()
		p.delConn(connID)
		return
	}
	p.pumpToMkring(connID, peer, conn, dstIP, uint16(port))
}

func (p *proxy) recvLoop(timeout time.Duration) {
	for {
		req := transportRecv{
			Channel:   mknetChannel,
			TimeoutMS: uint32(timeout / time.Millisecond),
		}
		if req.TimeoutMS == 0 {
			req.TimeoutMS = 1
		}
		if err := rawMkringTransport(p.trap, mkringTransportOpRecv, uintptr(unsafe.Pointer(&req))); err != nil {
			if errors.Is(err, syscall.ETIMEDOUT) || errors.Is(err, syscall.EAGAIN) {
				continue
			}
			log.Printf("mknet recv failed: %v", err)
			continue
		}
		if req.MessageLen == 0 {
			continue
		}
		msg, err := decodeNetMessage(req.Message[:req.MessageLen])
		if err != nil {
			log.Printf("mknet decode failed: %v", err)
			continue
		}
		p.handleRemote(req.PeerKernelID, msg)
	}
}

func (p *proxy) handleRemote(peer uint16, msg netMessage) {
	connID := msg.Header.ConnID
	switch msg.Header.MsgType {
	case mknetOpen:
		p.handleRemoteOpen(peer, msg)
	case mknetOpenOK:
		return
	case mknetOpenErr, mknetClose, mknetReset:
		if conn := p.delConn(connID); conn != nil {
			_ = conn.Close()
		}
	case mknetData:
		conn := p.getConn(connID)
		if conn == nil {
			_ = p.send(peer, mknetReset, connID, nil, msg.Header.DstIPBE, msg.Header.DstPortBE)
			return
		}
		payload := msg.Payload[:msg.Header.PayloadLen]
		if _, err := conn.Write(payload); err != nil {
			_ = p.send(peer, mknetReset, connID, nil, msg.Header.DstIPBE, msg.Header.DstPortBE)
			_ = conn.Close()
			p.delConn(connID)
		}
	}
}

func (p *proxy) handleRemoteOpen(peer uint16, msg netMessage) {
	port := int(msg.Header.DstPortBE)
	target := net.JoinHostPort(p.targetHost, strconv.Itoa(port))
	conn, err := net.Dial("tcp", target)
	if err != nil {
		log.Printf("mknet remote open conn=%d target=%s failed: %v", msg.Header.ConnID, target, err)
		_ = p.send(peer, mknetOpenErr, msg.Header.ConnID, nil, msg.Header.DstIPBE, msg.Header.DstPortBE)
		return
	}
	p.addConn(msg.Header.ConnID, peer, conn)
	_ = p.send(peer, mknetOpenOK, msg.Header.ConnID, nil, msg.Header.DstIPBE, msg.Header.DstPortBE)
	go p.pumpToMkring(msg.Header.ConnID, peer, conn, msg.Header.DstIPBE, msg.Header.DstPortBE)
}

func (p *proxy) pumpToMkring(connID uint64, peer uint16, conn net.Conn, dstIP uint32, dstPort uint16) {
	defer func() {
		_ = p.send(peer, mknetClose, connID, nil, dstIP, dstPort)
		_ = conn.Close()
		p.delConn(connID)
	}()

	buf := make([]byte, mknetMaxPayload)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if sendErr := p.send(peer, mknetData, connID, buf[:n], dstIP, dstPort); sendErr != nil {
				log.Printf("mknet data conn=%d peer=%d failed: %v", connID, peer, sendErr)
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("mknet local read conn=%d failed: %v", connID, err)
			}
			return
		}
	}
}

func (p *proxy) send(peer uint16, typ uint8, connID uint64, payload []byte, dstIP uint32, dstPort uint16) error {
	if len(payload) > mknetMaxPayload {
		return fmt.Errorf("payload too large: %d", len(payload))
	}
	msg := netMessage{}
	msg.Header = netHeader{
		Magic:      mknetMagic,
		Version:    mknetVersion,
		Channel:    mknetChannel,
		MsgType:    typ,
		ConnID:     connID,
		PayloadLen: uint32(len(payload)),
		DstIPBE:    dstIP,
		DstPortBE:  dstPort,
	}
	copy(msg.Payload[:], payload)
	data := encodeNetMessage(msg)
	req := transportSend{
		PeerKernelID: peer,
		Channel:      mknetChannel,
		MessageLen:   uint32(len(data)),
	}
	copy(req.Message[:], data)
	return rawMkringTransport(p.trap, mkringTransportOpSend, uintptr(unsafe.Pointer(&req)))
}

func (p *proxy) addConn(connID uint64, peer uint16, conn net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns[connID] = conn
	p.peers[connID] = peer
}

func (p *proxy) getConn(connID uint64) net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conns[connID]
}

func (p *proxy) delConn(connID uint64) net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	conn := p.conns[connID]
	delete(p.conns, connID)
	delete(p.peers, connID)
	return conn
}

func rawMkringTransport(trap uintptr, op uint32, arg uintptr) error {
	_, _, errno := syscall.Syscall(trap, uintptr(op), arg, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func encodeNetMessage(msg netMessage) []byte {
	buf := make([]byte, 36+mknetMaxPayload)
	binary.LittleEndian.PutUint32(buf[0:4], msg.Header.Magic)
	buf[4] = msg.Header.Version
	buf[5] = msg.Header.Channel
	buf[6] = msg.Header.MsgType
	buf[7] = msg.Header.Flags
	binary.LittleEndian.PutUint64(buf[8:16], msg.Header.ConnID)
	binary.LittleEndian.PutUint32(buf[16:20], msg.Header.Seq)
	binary.LittleEndian.PutUint32(buf[20:24], msg.Header.PayloadLen)
	binary.LittleEndian.PutUint32(buf[24:28], msg.Header.SrcIPBE)
	binary.LittleEndian.PutUint32(buf[28:32], msg.Header.DstIPBE)
	binary.LittleEndian.PutUint16(buf[32:34], msg.Header.SrcPortBE)
	binary.LittleEndian.PutUint16(buf[34:36], msg.Header.DstPortBE)
	copy(buf[36:], msg.Payload[:])
	return buf
}

func decodeNetMessage(data []byte) (netMessage, error) {
	if len(data) < 36 {
		return netMessage{}, fmt.Errorf("message too short: %d", len(data))
	}
	var msg netMessage
	msg.Header.Magic = binary.LittleEndian.Uint32(data[0:4])
	msg.Header.Version = data[4]
	msg.Header.Channel = data[5]
	msg.Header.MsgType = data[6]
	msg.Header.Flags = data[7]
	msg.Header.ConnID = binary.LittleEndian.Uint64(data[8:16])
	msg.Header.Seq = binary.LittleEndian.Uint32(data[16:20])
	msg.Header.PayloadLen = binary.LittleEndian.Uint32(data[20:24])
	msg.Header.SrcIPBE = binary.LittleEndian.Uint32(data[24:28])
	msg.Header.DstIPBE = binary.LittleEndian.Uint32(data[28:32])
	msg.Header.SrcPortBE = binary.LittleEndian.Uint16(data[32:34])
	msg.Header.DstPortBE = binary.LittleEndian.Uint16(data[34:36])
	if msg.Header.Magic != mknetMagic || msg.Header.Version != mknetVersion || msg.Header.Channel != mknetChannel {
		return netMessage{}, fmt.Errorf("invalid mknet header")
	}
	if msg.Header.PayloadLen > mknetMaxPayload {
		return netMessage{}, fmt.Errorf("payload too large: %d", msg.Header.PayloadLen)
	}
	copy(msg.Payload[:], data[36:])
	return msg, nil
}

func ipv4ToBE(host string) uint32 {
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip)
}
