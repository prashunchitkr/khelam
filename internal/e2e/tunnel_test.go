// Package e2e runs a real server, agent and local service on loopback
// and checks traffic end to end.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/agent"
	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/server"
)

const waitTimeout = 10 * time.Second

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// startEcho runs a service that echoes everything it reads and then
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

// startServer serves agents on listenAddr ("127.0.0.1:0" for any port)
// and returns the bound address and a channel that receives Serve's
// result once ctx is cancelled.
func startServer(t *testing.T, ctx context.Context, listenAddr string, portStart, portEnd uint32) (string, <-chan error) {
	t.Helper()

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New(server.Options{
		Logger: testLogger(t).With("component", "server"),
		Cfg: config.ServerConfig{
			PortRangeStart: portStart,
			PortRangeEnd:   portEnd,
		},
	})

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()

	return ln.Addr().String(), done
}

// startAgent runs an agent and returns a channel that receives the
// public port every time a tunnel is established.
func startAgent(t *testing.T, ctx context.Context, serverAddr, tunnelAddr string) <-chan uint32 {
	t.Helper()

	ports := make(chan uint32, 8)
	agt := agent.New(agent.Options{
		Logger: testLogger(t).With("component", "agent"),
		Cfg: config.AgentConfig{
			ServerAddress: serverAddr,
			TunnelAddress: tunnelAddr,
			Name:          t.Name(),
		},
		OnTunnel: func(port uint32) { ports <- port },
	})

	done := make(chan struct{})
	go func() {
		agt.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { <-done })

	return ports
}

func waitPort(t *testing.T, ports <-chan uint32) uint32 {
	t.Helper()

	select {
	case port := <-ports:
		return port
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for tunnel")
		return 0
	}
}

// assertEcho sends payload through the public port, half-closes, and
// checks the echoed bytes come back followed by EOF.
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

func TestTunnelEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	echoAddr := startEcho(t)
	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47100, 47109)
	port := waitPort(t, startAgent(t, ctx, serverAddr, echoAddr))

	t.Run("small", func(t *testing.T) {
		assertEcho(t, port, []byte("hello"))
	})

	t.Run("large", func(t *testing.T) {
		assertEcho(t, port, randomBytes(t, 4<<20))
	})

	t.Run("concurrent", func(t *testing.T) {
		for i := range 20 {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				assertEcho(t, port, randomBytes(t, 64<<10))
			})
		}
	})
}

func TestServerShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	serverAddr, serverDone := startServer(t, ctx, "127.0.0.1:0", 47110, 47119)
	waitPort(t, startAgent(t, ctx, serverAddr, startEcho(t)))

	cancel()

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("server did not shut down with an agent connected")
	}
}

func TestAgentReconnects(t *testing.T) {
	agentCtx, cancelAgent := context.WithCancel(t.Context())
	defer cancelAgent()

	echoAddr := startEcho(t)

	serverCtx, stopServer := context.WithCancel(t.Context())
	serverAddr, serverDone := startServer(t, serverCtx, "127.0.0.1:0", 47120, 47129)

	ports := startAgent(t, agentCtx, serverAddr, echoAddr)
	assertEcho(t, waitPort(t, ports), []byte("before"))

	stopServer()
	<-serverDone

	// Restart on the same address; the agent should find it again.
	restartCtx, stopRestart := context.WithCancel(t.Context())
	defer stopRestart()
	startServer(t, restartCtx, serverAddr, 47120, 47129)

	assertEcho(t, waitPort(t, ports), []byte("after"))
}

func TestSkipsBusyPublicPort(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Occupy the first port of the range, as another process might.
	busy, err := net.Listen("tcp", "0.0.0.0:47130")
	if err != nil {
		t.Skipf("cannot reserve test port: %v", err)
	}
	defer busy.Close()

	serverAddr, _ := startServer(t, ctx, "127.0.0.1:0", 47130, 47131)
	port := waitPort(t, startAgent(t, ctx, serverAddr, startEcho(t)))

	if port != 47131 {
		t.Fatalf("got public port %d, want 47131", port)
	}
	assertEcho(t, port, []byte("ok"))
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()

	b := make([]byte, n)
	rand.Read(b)
	return b
}
