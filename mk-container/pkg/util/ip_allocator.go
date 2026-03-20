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
	}, nil
}

// Allocate returns next available pod IP.
func (a *IPAllocator) Allocate() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	base := ipToUint32(a.network.IP)
	ones, bits := a.network.Mask.Size()
	hostBits := bits - ones
	maxHosts := uint32(1 << hostBits)

	if a.next >= maxHosts-1 {
		return "", fmt.Errorf("pod cidr exhausted")
	}

	ip := uint32ToIP(base + a.next)
	a.next++
	return ip.String(), nil
}
