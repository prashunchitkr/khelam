package server

import "testing"

func TestPortPoolInclusiveRange(t *testing.T) {
	pp := NewPortPool(100, 102)
	if got := pp.Free(); got != 3 {
		t.Fatalf("Free() = %d, want 3", got)
	}
}

func TestPortPoolFIFO(t *testing.T) {
	pp := NewPortPool(100, 102)

	first, _ := pp.Allocate(nil)
	if first != 100 {
		t.Fatalf("first Allocate() = %d, want 100", first)
	}

	// A released port must go to the back, not be handed out next.
	pp.Release(first)
	for _, want := range []uint32{101, 102, 100} {
		got, err := pp.Allocate(nil)
		if err != nil {
			t.Fatalf("Allocate: %v", err)
		}
		if got != want {
			t.Fatalf("Allocate() = %d, want %d", got, want)
		}
	}
}

func TestPortPoolExhausted(t *testing.T) {
	pp := NewPortPool(100, 100)
	pp.Allocate(nil)

	if _, err := pp.Allocate(nil); err == nil {
		t.Fatal("expected error from exhausted pool")
	}
}

func TestPortPoolIgnoresInvalidRelease(t *testing.T) {
	pp := NewPortPool(100, 101)
	port, _ := pp.Allocate(nil)

	pp.Release(port)
	pp.Release(port) // double release
	pp.Release(999)  // never allocated

	if got := pp.Free(); got != 2 {
		t.Fatalf("Free() = %d, want 2", got)
	}
}

func TestPortPoolAllocateSkips(t *testing.T) {
	pp := NewPortPool(100, 102)

	got, err := pp.Allocate(func(port uint32) bool { return port == 100 })
	if err != nil || got != 101 {
		t.Fatalf("Allocate(skip 100) = %d, %v; want 101", got, err)
	}

	if _, err := pp.Allocate(func(uint32) bool { return true }); err == nil {
		t.Fatal("expected error when every port is skipped")
	}
}

func TestPortPoolTake(t *testing.T) {
	pp := NewPortPool(100, 102)

	if !pp.Take(101) {
		t.Fatal("Take(101) on a free port failed")
	}
	if pp.Take(101) {
		t.Fatal("Take(101) succeeded twice")
	}
	if pp.Take(999) {
		t.Fatal("Take(999) succeeded for a port outside the pool")
	}

	pp.Release(101)
	if !pp.Take(101) {
		t.Fatal("Take(101) after release failed")
	}
}
