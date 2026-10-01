package relay

import (
	"io"
	"net"
	"testing"
	"time"
)

// tcpPair returns the two ends of a loopback TCP connection.
func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()

	dialed, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return dialed, <-accepted
}

// TestPipeHalfClose checks that EOF in one direction propagates while
// the other direction keeps flowing:
//
//	client <-> [left | Pipe | right] <-> service
func TestPipeHalfClose(t *testing.T) {
	client, left := tcpPair(t)
	right, service := tcpPair(t)
	defer client.Close()
	defer service.Close()

	done := make(chan struct{})
	go func() {
		Pipe(left, right)
		close(done)
	}()

	client.Write([]byte("request"))
	client.(*net.TCPConn).CloseWrite()

	// The service must see EOF after the request, without the
	// connection being torn down.
	service.SetDeadline(time.Now().Add(5 * time.Second))
	req, err := io.ReadAll(service)
	if err != nil || string(req) != "request" {
		t.Fatalf("service read %q, %v", req, err)
	}

	// The reverse direction must still work after the half-close.
	service.Write([]byte("response"))
	service.(*net.TCPConn).CloseWrite()

	client.SetDeadline(time.Now().Add(5 * time.Second))
	resp, err := io.ReadAll(client)
	if err != nil || string(resp) != "response" {
		t.Fatalf("client read %q, %v", resp, err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Pipe did not return after both directions finished")
	}
}
