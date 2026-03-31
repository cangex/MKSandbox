package agent

import (
	"sync"

	"mk-container/pkg/transport/mkringcontrol"
)

type MkringFactory struct {
	mu      sync.Mutex
	service *mkringcontrol.Service
	clients map[string]Client
}

func NewMkringFactory(syscallTrap uintptr) *MkringFactory {
	transport := mkringcontrol.NewUAPITransport(
		mkringcontrol.NewSyscallHostTransportUAPI(syscallTrap),
	)
	return newMkringFactory(transport)
}

func newMkringFactory(transport mkringcontrol.Transport) *MkringFactory {
	service := mkringcontrol.New(transport)
	return &MkringFactory{
		service: service,
		clients: map[string]Client{},
	}
}

func (f *MkringFactory) ForKernel(kernelID, endpoint string) Client {
	f.mu.Lock()
	defer f.mu.Unlock()

	cacheKey := kernelID + "|" + endpoint
	if c, ok := f.clients[cacheKey]; ok {
		return c
	}

	c, err := newMkringClient(kernelID, endpoint, f.service)
	if err != nil {
		ec := errorClient{err: err}
		f.clients[cacheKey] = ec
		return ec
	}

	f.clients[cacheKey] = c
	return c
}
