package e2e

import (
	"context"
	"errors"
	"testing"

	"github.com/prashunchitkr/khelam/internal/agent"
	"github.com/prashunchitkr/khelam/internal/config"
)

// TestStickyPortsAcrossAgentRestart checks that an agent gets its ports
// back after going offline, and that another agent does not take them
// while there are unreserved ports left.
func TestStickyPortsAcrossAgentRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47400, 47409)
	echo, udpEcho := startEcho(t), startUDPEcho(t)
	cfg := agentConfig(t, srv, "web=tcp://"+echo, "game=udp://"+udpEcho)

	firstCtx, stopFirst := context.WithCancel(ctx)
	assignments, result := startAgent(t, firstCtx, cfg)
	first := waitTunnels(t, assignments)
	stopFirst()
	waitResult(t, result)

	// A different agent must not be handed the offline agent's ports.
	other := agentConfig(t, srv, "web=tcp://"+echo, "game=udp://"+udpEcho)
	otherAssignments, _ := startAgent(t, ctx, other)
	otherTunnels := waitTunnels(t, otherAssignments)
	for _, name := range []string{"web", "game"} {
		if portOf(t, otherTunnels, name) == portOf(t, first, name) {
			t.Fatalf("tunnel %q of another agent got the offline agent's port %d", name, portOf(t, first, name))
		}
	}

	// The same agent ID gets the same ports back.
	assignments, _ = startAgent(t, ctx, cfg)
	again := waitTunnels(t, assignments)
	for _, name := range []string{"web", "game"} {
		if portOf(t, again, name) != portOf(t, first, name) {
			t.Fatalf("tunnel %q moved from port %d to %d", name, portOf(t, first, name), portOf(t, again, name))
		}
	}
}

// TestReservedPortReusedWhenFull checks that sticky ports never cost
// capacity: when only reserved ports are left, a new agent takes one.
func TestReservedPortReusedWhenFull(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47410, 47410)
	echo := startEcho(t)

	firstCtx, stopFirst := context.WithCancel(ctx)
	assignments, result := startAgent(t, firstCtx, agentConfig(t, srv, echo))
	waitPort(t, assignments)
	stopFirst()
	waitResult(t, result)

	assignments, _ = startAgent(t, ctx, agentConfig(t, srv, echo))
	if port := waitPort(t, assignments); port != 47410 {
		t.Fatalf("got port %d, want the only port 47410", port)
	}
}

// TestAgentTakeover checks that a second agent with the same ID replaces
// the first, keeping its ports, and that the first stops for good
// instead of fighting back.
func TestAgentTakeover(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47420, 47429)
	cfg := agentConfig(t, srv, startEcho(t))

	firstAssignments, firstResult := startAgent(t, ctx, cfg)
	port := waitPort(t, firstAssignments)

	secondAssignments, _ := startAgent(t, ctx, cfg)
	if got := waitPort(t, secondAssignments); got != port {
		t.Fatalf("replacement agent got port %d, want %d", got, port)
	}

	if err := waitResult(t, firstResult); !errors.Is(err, agent.ErrReplaced) {
		t.Fatalf("replaced agent Run() = %v, want ErrReplaced", err)
	}

	assertEcho(t, port, []byte("still works"))
}

// TestStickyPortsSurviveServerRestart checks reservations persist: after
// a restart the pool starts in port order, so an offline agent's port
// is first in line and must be skipped for a different agent.
func TestStickyPortsSurviveServerRestart(t *testing.T) {
	stateDir := t.TempDir()
	withState := func(c *config.ServerConfig) { c.StateDir = stateDir }
	echo := startEcho(t)

	firstCtx, stopFirst := context.WithCancel(t.Context())
	srv := startServer(t, firstCtx, "127.0.0.1:0", 47430, 47439, withState)
	owner := agentConfig(t, srv, echo)
	assignments, result := startAgent(t, firstCtx, owner)
	reserved := waitPort(t, assignments)
	stopFirst()
	<-srv.done
	waitResult(t, result)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	restarted := startServer(t, ctx, srv.addr, 47430, 47439, withState)

	// A different agent connects first and must skip the reserved port.
	otherAssignments, _ := startAgent(t, ctx, agentConfig(t, restarted, echo))
	if port := waitPort(t, otherAssignments); port == reserved {
		t.Fatalf("another agent was given reserved port %d", reserved)
	}

	assignments, _ = startAgent(t, ctx, owner)
	if port := waitPort(t, assignments); port != reserved {
		t.Fatalf("owner got port %d after restart, want %d", port, reserved)
	}
}
