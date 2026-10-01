// Package e2e runs a real server, agent and local services on loopback
// and checks traffic end to end.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/agent"
	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/server"
)

const waitTimeout = 10 * time.Second

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testServer struct {
	addr        string
	fingerprint string
	// done receives Serve's result once its context is cancelled.
	done <-chan error
}

// startServer serves agents on listenAddr ("127.0.0.1:0" for any port).
// opts may adjust the config.
func startServer(t *testing.T, ctx context.Context, listenAddr string, portStart, portEnd uint32, opts ...func(*config.ServerConfig)) testServer {
	t.Helper()

	cfg := config.ServerConfig{
		PortRangeStart: portStart,
		PortRangeEnd:   portEnd,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	srv, err := server.New(server.Options{
		Logger: testLogger(t).With("component", "server"),
		Cfg:    cfg,
	})
	if err != nil {
		t.Fatal(err)
	}

	pc, err := net.ListenPacket("udp", listenAddr)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- srv.Serve(ctx, pc)
		close(finished)
	}()
	// The server logs through t, so it must stop before the test ends.
	t.Cleanup(func() { <-finished })

	return testServer{addr: pc.LocalAddr().String(), fingerprint: srv.Fingerprint(), done: done}
}

func newAgentID(t *testing.T) string {
	t.Helper()

	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// agentConfig returns a config for an agent that pins srv's certificate
// and exposes the given tunnel specs.
func agentConfig(t *testing.T, srv testServer, tunnels ...string) config.AgentConfig {
	t.Helper()

	cfg := config.AgentConfig{
		ServerAddress: srv.addr,
		Name:          t.Name(),
		AgentID:       newAgentID(t),
		Fingerprint:   srv.fingerprint,
	}

	for _, spec := range tunnels {
		tc, err := config.ParseTunnel(spec)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Tunnels = append(cfg.Tunnels, tc)
	}

	return cfg
}

// startAgent runs an agent until ctx is cancelled. It returns a channel
// receiving the tunnel assignments on every registration, and one
// receiving Run's result.
func startAgent(t *testing.T, ctx context.Context, cfg config.AgentConfig) (<-chan []protocol.TunnelAssignment, <-chan error) {
	t.Helper()

	assignments := make(chan []protocol.TunnelAssignment, 8)
	agt := agent.New(agent.Options{
		Logger:   testLogger(t).With("component", "agent"),
		Cfg:      cfg,
		OnTunnel: func(tunnels []protocol.TunnelAssignment) { assignments <- tunnels },
	})

	result := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		result <- agt.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { <-done })

	return assignments, result
}

func waitTunnels(t *testing.T, assignments <-chan []protocol.TunnelAssignment) []protocol.TunnelAssignment {
	t.Helper()

	select {
	case tunnels := <-assignments:
		return tunnels
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for tunnels")
		return nil
	}
}

// waitPort waits for registration and returns the public port of the
// agent's first tunnel.
func waitPort(t *testing.T, assignments <-chan []protocol.TunnelAssignment) uint32 {
	t.Helper()
	return waitTunnels(t, assignments)[0].PublicPort
}

func waitResult(t *testing.T, result <-chan error) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for agent to stop")
		return nil
	}
}

// startEcho runs a TCP service that echoes everything it reads and then
// half-closes, and returns its address.
func startEcho(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				io.Copy(conn, conn)
				conn.(*net.TCPConn).CloseWrite()
			}()
		}
	}()

	return ln.Addr().String()
}

// startUDPEcho runs a UDP service that replies to each packet with
// "<sender address>|<payload>", and returns its address.
func startUDPEcho(t *testing.T) string {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			conn.WriteTo(fmt.Appendf(nil, "%s|%s", from, buf[:n]), from)
		}
	}()

	return conn.LocalAddr().String()
}

// assertEcho sends payload through the public TCP port, half-closes,
// and checks the echoed bytes come back followed by EOF.
func assertEcho(t *testing.T, port uint32, payload []byte) {
	t.Helper()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial public port: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(waitTimeout))

	writeErr := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		conn.(*net.TCPConn).CloseWrite()
		writeErr <- err
	}()

	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: sent %d bytes, got %d", len(payload), len(got))
	}
}

// udpExchange sends payload from conn and returns the reply payload and
// the sender address the service saw. UDP may drop packets, so it
// retries a few times.
func udpExchange(t *testing.T, conn net.Conn, payload string) (from, reply string) {
	t.Helper()

	buf := make([]byte, 64*1024)
	for range 5 {
		conn.Write([]byte(payload))
		conn.SetReadDeadline(time.Now().Add(time.Second))

		n, err := conn.Read(buf)
		if err != nil {
			continue
		}

		from, reply, ok := bytes.Cut(buf[:n], []byte("|"))
		if !ok {
			t.Fatalf("malformed reply %q", buf[:n])
		}
		return string(from), string(reply)
	}

	t.Fatalf("no UDP reply for %q", payload)
	return "", ""
}

func dialUDP(t *testing.T, port uint32) net.Conn {
	t.Helper()

	conn, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()

	b := make([]byte, n)
	rand.Read(b)
	return b
}

func portOf(t *testing.T, tunnels []protocol.TunnelAssignment, name string) uint32 {
	t.Helper()

	for _, tunnel := range tunnels {
		if tunnel.Name == name {
			return tunnel.PublicPort
		}
	}
	t.Fatalf("no tunnel named %q in %+v", name, tunnels)
	return 0
}
