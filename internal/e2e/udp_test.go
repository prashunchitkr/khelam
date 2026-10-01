package e2e

import (
	"context"
	"fmt"
	"testing"
)

func TestUDPEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47200, 47209)
	assignments, _ := startAgent(t, ctx, agentConfig(t, srv, "udp://"+startUDPEcho(t)))
	client := dialUDP(t, waitPort(t, assignments))

	for i := range 10 {
		payload := fmt.Sprintf("packet-%d", i)
		if _, reply := udpExchange(t, client, payload); reply != payload {
			t.Fatalf("reply = %q, want %q", reply, payload)
		}
	}
}

// TestUDPFlowsAreSeparate checks that each public client gets its own
// flow: the local service sees a distinct sender per client, and replies
// go back only to the client that sent the request.
func TestUDPFlowsAreSeparate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47210, 47219)
	assignments, _ := startAgent(t, ctx, agentConfig(t, srv, "udp://"+startUDPEcho(t)))
	port := waitPort(t, assignments)

	alice, bob := dialUDP(t, port), dialUDP(t, port)

	aliceFrom, aliceReply := udpExchange(t, alice, "from alice")
	bobFrom, bobReply := udpExchange(t, bob, "from bob")

	if aliceReply != "from alice" || bobReply != "from bob" {
		t.Fatalf("replies crossed: alice got %q, bob got %q", aliceReply, bobReply)
	}
	if aliceFrom == bobFrom {
		t.Fatalf("both clients reached the service from %s; want separate flows", aliceFrom)
	}

	// A flow keeps its local socket across packets.
	if again, _ := udpExchange(t, alice, "again"); again != aliceFrom {
		t.Fatalf("alice's flow changed sender %s -> %s", aliceFrom, again)
	}
}

func TestMixedTunnels(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47220, 47229)
	cfg := agentConfig(t, srv,
		"web=tcp://"+startEcho(t),
		"game=udp://"+startUDPEcho(t),
		"admin=tcp://"+startEcho(t),
	)
	assignments, _ := startAgent(t, ctx, cfg)
	tunnels := waitTunnels(t, assignments)

	web, game, admin := portOf(t, tunnels, "web"), portOf(t, tunnels, "game"), portOf(t, tunnels, "admin")
	if web == admin {
		t.Fatalf("two TCP tunnels share port %d", web)
	}

	assertEcho(t, web, []byte("web"))
	assertEcho(t, admin, []byte("admin"))
	if _, reply := udpExchange(t, dialUDP(t, game), "game"); reply != "game" {
		t.Fatalf("UDP reply = %q", reply)
	}
}
