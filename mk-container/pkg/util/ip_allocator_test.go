package util

import "testing"

func TestIPAllocatorAllocateRelease(t *testing.T) {
	a, err := NewIPAllocator("10.240.0.0", 24)
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
	if first != "10.240.0.2" || second != "10.240.0.3" {
		t.Fatalf("unexpected allocation order: got %s, %s", first, second)
	}

	a.Release(first)
	reused, err := a.Allocate()
	if err != nil {
		t.Fatalf("allocate reused ip: %v", err)
	}
	if reused != first {
		t.Fatalf("expected reused ip %s, got %s", first, reused)
	}
}

func TestIPAllocatorReleaseIgnoresInvalidIPs(t *testing.T) {
	a, err := NewIPAllocator("10.240.0.0", 24)
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

	a.Release("not-an-ip")
	a.Release("10.241.0.2")
	a.Release("10.240.0.1")
	a.Release(first)
	a.Release(first)

	reused, err := a.Allocate()
	if err != nil {
		t.Fatalf("allocate reused ip: %v", err)
	}
	if reused != first {
		t.Fatalf("expected reused ip %s, got %s", first, reused)
	}

	next, err := a.Allocate()
	if err != nil {
		t.Fatalf("allocate next ip: %v", err)
	}
	if next != "10.240.0.4" {
		t.Fatalf("expected next sequential ip 10.240.0.4, got %s (second was %s)", next, second)
	}
}

func TestIPAllocatorExhaustion(t *testing.T) {
	a, err := NewIPAllocator("10.240.0.0", 30)
	if err != nil {
		t.Fatalf("new allocator: %v", err)
	}

	ip, err := a.Allocate()
	if err != nil {
		t.Fatalf("allocate only usable ip: %v", err)
	}
	if ip != "10.240.0.2" {
		t.Fatalf("expected only usable ip 10.240.0.2, got %s", ip)
	}

	if _, err := a.Allocate(); err == nil {
		t.Fatalf("expected allocator exhaustion")
	}
}
