package server

import (
	"path/filepath"
	"testing"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
)

func TestReservationsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	key := reservationKey{AgentID: "a", Tunnel: "mc", Protocol: protocol.ProtocolTCP}

	r, err := LoadReservations(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Reserve(key, 41000); err != nil {
		t.Fatal(err)
	}

	reloaded, err := LoadReservations(path)
	if err != nil {
		t.Fatal(err)
	}
	if port, ok := reloaded.Lookup(key); !ok || port != 41000 {
		t.Fatalf("Lookup after reload = %d, %v; want 41000", port, ok)
	}
}

func TestReservationsOnePortOneOwner(t *testing.T) {
	r, _ := LoadReservations("")
	a := reservationKey{AgentID: "a", Tunnel: "t", Protocol: protocol.ProtocolTCP}
	b := reservationKey{AgentID: "b", Tunnel: "t", Protocol: protocol.ProtocolTCP}

	r.Reserve(a, 41000)
	if !r.ReservedByOther(b, 41000) || r.ReservedByOther(a, 41000) {
		t.Fatal("41000 should be reserved by a only")
	}

	// b takes over a's port; a loses its reservation.
	r.Reserve(b, 41000)
	if _, ok := r.Lookup(a); ok {
		t.Fatal("a still holds a reservation after b took its port")
	}

	// Moving b to another port frees 41000.
	r.Reserve(b, 41001)
	if r.ReservedByOther(a, 41000) {
		t.Fatal("41000 still reserved after its owner moved")
	}
}

func TestReservationsSeparatePerProtocol(t *testing.T) {
	r, _ := LoadReservations("")
	tcp := reservationKey{AgentID: "a", Tunnel: "t", Protocol: protocol.ProtocolTCP}
	udp := reservationKey{AgentID: "b", Tunnel: "t", Protocol: protocol.ProtocolUDP}

	r.Reserve(tcp, 41000)
	if r.ReservedByOther(udp, 41000) {
		t.Fatal("a TCP reservation must not block the same UDP port")
	}
}
