// Package agent implements the client side of a tunnel: it registers
// with the server and relays each incoming stream to a local service.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/relay"
)

const (
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 5 * time.Second
)

// Options carries the dependencies required to construct an Agent.
type Options struct {
	Logger *slog.Logger
	Cfg    config.AgentConfig
}

// Agent maintains a single tunnel session with the server.
type Agent struct {
	logger *slog.Logger
	cfg    config.AgentConfig
}

func New(options Options) *Agent {
	return &Agent{
		logger: options.Logger,
		cfg:    options.Cfg,
	}
}

// Run registers with the server and relays tunnel traffic until ctx is
// cancelled or the session ends. It returns nil only when ctx is
// cancelled.
func (a *Agent) Run(ctx context.Context) error {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", a.cfg.ServerAddress)
	if err != nil {
		return fmt.Errorf("cannot connect to server %s: %w", a.cfg.ServerAddress, err)
	}
	defer conn.Close()

	publicPort, err := a.handshake(conn)
	if err != nil {
		return err
	}

	a.logger.Info("tunnel established", "agentName", a.cfg.Name, "publicPort", publicPort, "tunnelAddr", a.cfg.TunnelAddress)

	// After the OK reply the server switches the connection to yamux,
	// so the agent must do the same.
	session, err := yamux.Client(conn, yamux.DefaultConfig())
	if err != nil {
		return fmt.Errorf("cannot start yamux session: %w", err)
	}
	defer session.Close()

	go func() {
		select {
		case <-ctx.Done():
			session.Close()
		case <-session.CloseChan():
		}
	}()

	for {
		stream, err := session.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tunnel session ended: %w", err)
		}

		go a.handleStream(stream)
	}
}

// handshake registers the agent and returns the assigned public port.
func (a *Agent) handshake(conn net.Conn) (uint32, error) {
	conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	if err := protocol.WriteMessage(conn, protocol.KindRegister, protocol.RegisterMessage{
		Name: a.cfg.Name,
	}); err != nil {
		return 0, fmt.Errorf("failed to send register message: %w", err)
	}

	reply, err := protocol.ReadMessage(conn)
	if err != nil {
		return 0, fmt.Errorf("failed to read handshake reply: %w", err)
	}

	switch msg := reply.(type) {
	case protocol.OkMessage:
		return msg.PublicPort, nil
	case protocol.ErrorMessage:
		return 0, fmt.Errorf("server rejected registration: %s", msg.Reason)
	default:
		return 0, errors.New("unexpected handshake reply")
	}
}

// handleStream relays one tunnelled connection to the local service.
func (a *Agent) handleStream(stream net.Conn) {
	localConn, err := net.DialTimeout("tcp", a.cfg.TunnelAddress, dialTimeout)
	if err != nil {
		a.logger.Error("failed to connect to local service", "tunnelAddr", a.cfg.TunnelAddress, "error", err)
		stream.Close()
		return
	}

	relay.Pipe(stream, localConn)
}
