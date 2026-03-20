package util

import "testing"

func TestIntAllocatorAllocateRelease(t *testing.T) {
	a, err := NewIntAllocator(1, 2)
	if err != nil {
		t.Fatalf("new allocator: %v", err)
	}

	first, err := a.Allocate()
	if err != nil {
		t.Fatalf("allocate first: %v", err)
	}
	second, err := a.Allocate()
	if err != nil {
		t.Fatalf("allocate second: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("unexpected allocation order: got %d, %d", first, second)
	}

	if _, err := a.Allocate(); err == nil {
		t.Fatalf("expected allocator exhaustion")
	}

	a.Release(first)
	reused, err := a.Allocate()
	if err != nil {
		t.Fatalf("allocate reused slot: %v", err)
	}
	if reused != first {
		t.Fatalf("expected reused slot %d, got %d", first, reused)
	}
}
