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

	first, _ := pp.Allocate()
	if first != 100 {
		t.Fatalf("first Allocate() = %d, want 100", first)
	}

	// A released port must go to the back, not be handed out next.
	pp.Release(first)
	for _, want := range []uint32{101, 102, 100} {
		got, err := pp.Allocate()
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
	pp.Allocate()

	if _, err := pp.Allocate(); err == nil {
		t.Fatal("expected error from exhausted pool")
	}
}

func TestPortPoolIgnoresInvalidRelease(t *testing.T) {
	pp := NewPortPool(100, 101)
	port, _ := pp.Allocate()

	pp.Release(port)
	pp.Release(port) // double release
	pp.Release(999)  // never allocated

	if got := pp.Free(); got != 2 {
		t.Fatalf("Free() = %d, want 2", got)
	}
}
