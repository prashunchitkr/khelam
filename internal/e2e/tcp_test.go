package e2e

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/prashunchitkr/khelam/internal/config"
)

func TestTunnelEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47100, 47109)
	assignments, _ := startAgent(t, ctx, agentConfig(t, srv, startEcho(t)))
	port := waitPort(t, assignments)

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

	srv := startServer(t, ctx, "127.0.0.1:0", 47110, 47119)
	assignments, _ := startAgent(t, ctx, agentConfig(t, srv, startEcho(t)))
	waitPort(t, assignments)

	cancel()

	select {
	case err := <-srv.done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(waitTimeout):
		t.Fatal("server did not shut down with an agent connected")
	}
}

func TestAgentReconnectsKeepsPorts(t *testing.T) {
	agentCtx, cancelAgent := context.WithCancel(t.Context())
	defer cancelAgent()

	// The state directory keeps the certificate and reservations across
	// the restart, so the pinned fingerprint and ports stay valid.
	stateDir := t.TempDir()
	withState := func(c *config.ServerConfig) { c.StateDir = stateDir }

	serverCtx, stopServer := context.WithCancel(t.Context())
	srv := startServer(t, serverCtx, "127.0.0.1:0", 47120, 47129, withState)

	assignments, _ := startAgent(t, agentCtx, agentConfig(t, srv, startEcho(t)))
	before := waitPort(t, assignments)
	assertEcho(t, before, []byte("before"))

	stopServer()
	<-srv.done

	restartCtx, stopRestart := context.WithCancel(t.Context())
	defer stopRestart()
	restarted := startServer(t, restartCtx, srv.addr, 47120, 47129, withState)
	if restarted.fingerprint != srv.fingerprint {
		t.Fatal("self-signed certificate changed across restart")
	}

	after := waitPort(t, assignments)
	if after != before {
		t.Fatalf("public port changed across reconnect: %d -> %d", before, after)
	}
	assertEcho(t, after, []byte("after"))
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

	srv := startServer(t, ctx, "127.0.0.1:0", 47130, 47131)
	assignments, _ := startAgent(t, ctx, agentConfig(t, srv, startEcho(t)))
	port := waitPort(t, assignments)

	if port != 47131 {
		t.Fatalf("got public port %d, want 47131", port)
	}
	assertEcho(t, port, []byte("ok"))
}

func TestProxyProtocol(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The local service reads the PROXY v2 header for an IPv4 client,
	// reports the source address, then echoes the remaining bytes.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	sources := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		header := make([]byte, 16+12)
		if _, err := io.ReadFull(conn, header); err != nil {
			sources <- "read error: " + err.Error()
			return
		}
		if !bytes.Equal(header[:12], []byte("\r\n\r\n\x00\r\nQUIT\n")) || header[13] != 0x11 {
			sources <- fmt.Sprintf("bad header %x", header)
			return
		}
		ip := net.IP(header[16:20])
		port := binary.BigEndian.Uint16(header[24:26])
		sources <- fmt.Sprintf("%s:%d", ip, port)

		io.Copy(conn, conn)
		conn.(*net.TCPConn).CloseWrite()
	}()

	srv := startServer(t, ctx, "127.0.0.1:0", 47140, 47149)
	cfg := agentConfig(t, srv, ln.Addr().String())
	cfg.ProxyProtocol = true
	assignments, _ := startAgent(t, ctx, cfg)
	publicPort := waitPort(t, assignments)

	client, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", publicPort))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.SetDeadline(time.Now().Add(waitTimeout))

	client.Write([]byte("payload"))
	client.(*net.TCPConn).CloseWrite()

	select {
	case got := <-sources:
		if want := client.LocalAddr().String(); got != want {
			t.Fatalf("service saw client %s, want %s", got, want)
		}
	case <-time.After(waitTimeout):
		t.Fatal("service never received a connection")
	}

	// The data after the header must be untouched.
	echoed, err := io.ReadAll(client)
	if err != nil || string(echoed) != "payload" {
		t.Fatalf("echo = %q, %v", echoed, err)
	}
}

func TestMaxConnsPerTunnel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	srv := startServer(t, ctx, "127.0.0.1:0", 47150, 47159,
		func(c *config.ServerConfig) { c.MaxConnsPerTunnel = 1 })
	assignments, _ := startAgent(t, ctx, agentConfig(t, srv, startEcho(t)))
	publicAddr := fmt.Sprintf("127.0.0.1:%d", waitPort(t, assignments))

	// Hold one connection open and prove it is relayed.
	first, err := net.Dial("tcp", publicAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	first.SetDeadline(time.Now().Add(waitTimeout))
	first.Write([]byte("x"))
	if _, err := io.ReadFull(first, make([]byte, 1)); err != nil {
		t.Fatalf("first connection not relayed: %v", err)
	}

	// The second must be closed by the server straight away.
	second, err := net.Dial("tcp", publicAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	second.SetDeadline(time.Now().Add(waitTimeout))
	if n, err := second.Read(make([]byte, 1)); err == nil {
		t.Fatalf("second connection read %d bytes, want it closed", n)
	} else if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("second connection was not closed")
	}

	// Releasing the first frees the slot again.
	first.Close()
	deadline := time.Now().Add(waitTimeout)
	for {
		conn, err := net.Dial("tcp", publicAddr)
		if err == nil {
			conn.SetDeadline(time.Now().Add(time.Second))
			conn.Write([]byte("y"))
			_, err = io.ReadFull(conn, make([]byte, 1))
			conn.Close()
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("slot was not released after the first connection closed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
