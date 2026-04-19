package util

import (
	"fmt"
	"net"
	"sync"
)

// IPAllocator allocates sequential IPv4 addresses from a CIDR.
type IPAllocator struct {
	mu      sync.Mutex
	network *net.IPNet
	next    uint32
	free    []uint32
	used    map[uint32]struct{}
}

func ipToUint32(ip net.IP) uint32 {
	v := ip.To4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

func uint32ToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// NewIPAllocator creates an allocator for baseIP/mask. Example: 10.240.0.0/24.
func NewIPAllocator(baseIP string, mask int) (*IPAllocator, error) {
	ip := net.ParseIP(baseIP)
	if ip == nil {
		return nil, fmt.Errorf("invalid base ip: %s", baseIP)
	}
	_, network, err := net.ParseCIDR(fmt.Sprintf("%s/%d", ip.String(), mask))
	if err != nil {
		return nil, err
	}

	// Skip network address (.0) and gateway (.1).
	return &IPAllocator{
		network: network,
		next:    2,
		used:    map[uint32]struct{}{},
	}, nil
}

func (a *IPAllocator) CIDR() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.network == nil {
		return ""
	}
	return a.network.String()
}

// Allocate returns next available pod IP.
func (a *IPAllocator) Allocate() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if n := len(a.free); n > 0 {
		offset := a.free[n-1]
		a.free = a.free[:n-1]
		a.used[offset] = struct{}{}
		return uint32ToIP(ipToUint32(a.network.IP) + offset).String(), nil
	}

	base := ipToUint32(a.network.IP)
	ones, bits := a.network.Mask.Size()
	hostBits := bits - ones
	maxHosts := uint32(1 << hostBits)

	if a.next >= maxHosts-1 {
		return "", fmt.Errorf("pod cidr exhausted")
	}

	ip := uint32ToIP(base + a.next)
	a.used[a.next] = struct{}{}
	a.next++
	return ip.String(), nil
}

// Release returns an allocated pod IP back to the allocator.
func (a *IPAllocator) Release(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return
	}

	ipv4 := parsed.To4()
	if ipv4 == nil || !a.network.Contains(ipv4) {
		return
	}

	base := ipToUint32(a.network.IP)
	offset := ipToUint32(ipv4) - base

	ones, bits := a.network.Mask.Size()
	hostBits := bits - ones
	maxHosts := uint32(1 << hostBits)

	if offset < 2 || offset >= maxHosts-1 {
		return
	}
	if _, ok := a.used[offset]; !ok {
		return
	}

	delete(a.used, offset)
	a.free = append(a.free, offset)
}
