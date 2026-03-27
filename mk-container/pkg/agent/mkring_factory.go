package agent

import (
	"sync"

	"mk-container/pkg/transport/mkringcontrol"
)

type MkringFactory struct {
	mu         sync.Mutex
	devicePath string
	service    *mkringcontrol.Service
	clients    map[string]Client
}

func NewMkringFactory(devicePath string) *MkringFactory {
	transport := mkringcontrol.NewDeviceTransport(devicePath)
	service := mkringcontrol.New(transport)
	return &MkringFactory{
		devicePath: devicePath,
		service:    service,
		clients:    map[string]Client{},
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
