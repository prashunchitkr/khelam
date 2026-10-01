// Package agent implements the client side of a tunnel: it registers
// with the server and relays each incoming stream to a local service.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
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

	minBackoff = 1 * time.Second
	maxBackoff = 30 * time.Second
)

// Options carries the dependencies required to construct an Agent.
type Options struct {
	Logger *slog.Logger
	Cfg    config.AgentConfig

	// OnTunnel, if set, is called with the assigned public port each
	// time a tunnel is established.
	OnTunnel func(publicPort uint32)
}

// Agent maintains a tunnel session with the server, reconnecting
// whenever it is lost.
type Agent struct {
	logger   *slog.Logger
	cfg      config.AgentConfig
	onTunnel func(publicPort uint32)
}

func New(options Options) *Agent {
	return &Agent{
		logger:   options.Logger,
		cfg:      options.Cfg,
		onTunnel: options.OnTunnel,
	}
}

// Run keeps a tunnel session alive until ctx is cancelled, reconnecting
// with exponential backoff whenever the session fails or ends.
func (a *Agent) Run(ctx context.Context) error {
	backoff := minBackoff

	for {
		established, err := a.runSession(ctx)
		if ctx.Err() != nil {
			return nil
		}

		// A session that got as far as a tunnel was healthy, so the
		// next failure starts the backoff over.
		if established {
			backoff = minBackoff
		}

		wait := jitter(backoff)
		a.logger.Warn("tunnel lost, reconnecting", "error", err, "retryIn", wait)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

// jitter spreads d over [d/2, d) so agents do not reconnect in lockstep.
func jitter(d time.Duration) time.Duration {
	half := d / 2
	return half + rand.N(half)
}

// runSession registers with the server and relays tunnel traffic until
// ctx is cancelled or the session ends. established reports whether the
// tunnel was set up before the session ended.
func (a *Agent) runSession(ctx context.Context) (established bool, err error) {
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", a.cfg.ServerAddress)
	if err != nil {
		return false, fmt.Errorf("cannot connect to server %s: %w", a.cfg.ServerAddress, err)
	}
	defer conn.Close()

	// Closing the connection on cancellation unblocks the handshake and
	// tears down the yamux session built on top of it.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	publicPort, err := a.handshake(conn)
	if err != nil {
		return false, err
	}

	a.logger.Info("tunnel established", "agentName", a.cfg.Name, "publicPort", publicPort, "tunnelAddr", a.cfg.TunnelAddress)

	// After the OK reply the server switches the connection to yamux,
	// so the agent must do the same.
	session, err := yamux.Client(conn, yamux.DefaultConfig())
	if err != nil {
		return false, fmt.Errorf("cannot start yamux session: %w", err)
	}
	defer session.Close()

	if a.onTunnel != nil {
		a.onTunnel(publicPort)
	}

	for {
		stream, err := session.Accept()
		if err != nil {
			return true, fmt.Errorf("tunnel session ended: %w", err)
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
