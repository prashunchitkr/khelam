package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/prashunchitkr/khelam/internal/agent"
)

// waitStatus polls the agent until cond holds for its status.
func waitStatus(t *testing.T, agt *agent.Agent, what string, cond func(agent.Status) bool) agent.Status {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)
	for {
		status := agt.Status()
		if cond(status) {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last status %+v", what, status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAgentStatusCounters(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47600, 47609)
	cfg := agentConfig(t, srv, "web=tcp://"+startEcho(t), "game=udp://"+startUDPEcho(t))
	agt, assignments, _ := startAgentHandle(t, ctx, cfg)
	tunnels := waitTunnels(t, assignments)

	status := waitStatus(t, agt, "connected", func(s agent.Status) bool { return s.State == agent.StateConnected })
	for i, name := range []string{"web", "game"} {
		want := fmt.Sprintf("127.0.0.1:%d", portOf(t, tunnels, name))
		if got := status.Tunnels[i]; got.Name != name || got.PublicAddress != want {
			t.Fatalf("tunnel %d = %+v, want %s at %s", i, got, name, want)
		}
	}

	// One TCP connection, fully closed afterwards.
	payload := randomBytes(t, 1000)
	assertEcho(t, portOf(t, tunnels, "web"), payload)
	waitStatus(t, agt, "TCP counters", func(s agent.Status) bool {
		web := s.Tunnels[0]
		return web.Total == 1 && web.Active == 0 && web.BytesIn == 1000 && web.BytesOut == 1000
	})

	// One UDP flow, still open. The service replies "<sender>|ping".
	client := dialUDP(t, portOf(t, tunnels, "game"))
	from, _ := udpExchange(t, client, "ping")
	replySize := int64(len(from) + len("|ping"))
	waitStatus(t, agt, "UDP counters", func(s agent.Status) bool {
		game := s.Tunnels[1]
		return game.Total == 1 && game.Active == 1 && game.BytesIn >= 4 && game.BytesOut >= replySize
	})
}

func TestAgentStatusRetryingAndRetryNow(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The only port is taken, so the second agent is refused with a
	// retryable no_capacity error on every attempt.
	srv := startServer(t, ctx, "127.0.0.1:0", 47610, 47610)
	echo := startEcho(t)
	assignments, _ := startAgent(t, ctx, agentConfig(t, srv, echo))
	waitPort(t, assignments)

	agentCtx, stopAgent := context.WithCancel(ctx)
	agt, _, _ := startAgentHandle(t, agentCtx, agentConfig(t, srv, echo))

	first := waitStatus(t, agt, "retrying", func(s agent.Status) bool { return s.State == agent.StateRetrying })
	if first.Err == nil || !first.RetryAt.After(first.Since) {
		t.Fatalf("retrying status = %+v, want an error and a later RetryAt", first)
	}

	// Wait for a later attempt, whose backoff is at least a second, then
	// cut it short.
	second := waitStatus(t, agt, "second retry", func(s agent.Status) bool {
		return s.State == agent.StateRetrying && s.Since.After(first.Since) && time.Until(s.RetryAt) > 500*time.Millisecond
	})
	agt.RetryNow()
	waitStatus(t, agt, "early retry", func(s agent.Status) bool { return s.Since.After(second.Since) })
	if time.Now().After(second.RetryAt) {
		t.Fatal("RetryNow did not cut the wait short")
	}

	stopAgent()
	waitStatus(t, agt, "stopped", func(s agent.Status) bool { return s.State == agent.StateStopped })
}
