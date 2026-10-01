// Package transport multiplexes many tunnelled streams over a single
// agent connection. Callers depend only on Session, so the underlying
// multiplexer (currently yamux) can be replaced, e.g. by QUIC.
package transport

import (
	"io"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

const keepAliveInterval = 30 * time.Second

// Session is a multiplexed connection between the server and an agent.
// Either side may open streams.
type Session interface {
	// Open starts a new stream to the peer.
	Open() (net.Conn, error)

	// Accept waits for the next stream opened by the peer.
	Accept() (net.Conn, error)

	// Close tears down the session and every stream on it.
	Close() error

	// CloseChan is closed once the session has ended.
	CloseChan() <-chan struct{}
}

// Server starts the server side of a session over conn.
func Server(conn net.Conn) (Session, error) {
	return yamux.Server(conn, yamuxConfig())
}

// Client starts the agent side of a session over conn.
func Client(conn net.Conn) (Session, error) {
	return yamux.Client(conn, yamuxConfig())
}

func yamuxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = keepAliveInterval
	// Errors are returned to callers, which log them through slog.
	cfg.LogOutput = io.Discard
	return cfg
}
