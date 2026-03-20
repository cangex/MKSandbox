package util

import (
	"fmt"
	"sync"
)

// IntAllocator allocates integer slots from a fixed inclusive range.
type IntAllocator struct {
	mu   sync.Mutex
	min  int
	max  int
	next int
	free []int
	used map[int]struct{}
}

func NewIntAllocator(min, max int) (*IntAllocator, error) {
	if min < 0 || max < min {
		return nil, fmt.Errorf("invalid range: [%d, %d]", min, max)
	}

	return &IntAllocator{
		min:  min,
		max:  max,
		next: min,
		used: map[int]struct{}{},
	}, nil
}

func (a *IntAllocator) Allocate() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if n := len(a.free); n > 0 {
		id := a.free[n-1]
		a.free = a.free[:n-1]
		a.used[id] = struct{}{}
		return id, nil
	}

	if a.next > a.max {
		return 0, fmt.Errorf("allocator exhausted")
	}

	id := a.next
	a.next++
	a.used[id] = struct{}{}
	return id, nil
}

func (a *IntAllocator) Release(id int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if id < a.min || id > a.max {
		return
	}
	if _, ok := a.used[id]; !ok {
		return
	}

	delete(a.used, id)
	a.free = append(a.free, id)
}
