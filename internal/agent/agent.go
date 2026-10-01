// Package agent implements the client side of a tunnel: it registers
// its tunnels with the server and relays their traffic to local
// services.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"time"

	"github.com/prashunchitkr/khelam/internal/config"
	"github.com/prashunchitkr/khelam/internal/protocol"
	"github.com/prashunchitkr/khelam/internal/tlsutil"
	"github.com/prashunchitkr/khelam/internal/transport"
)

const (
	dialTimeout      = 10 * time.Second
	handshakeTimeout = 5 * time.Second

	minBackoff = 1 * time.Second
	maxBackoff = 30 * time.Second
)

// ErrReplaced is returned when another agent with the same ID connects
// to the server, which then drops this one.
var ErrReplaced = errors.New("another agent with the same ID connected to the server")

// Options carries the dependencies required to construct an Agent.
type Options struct {
	Logger *slog.Logger
	Cfg    config.AgentConfig

	// OnTunnel, if set, is called with the assigned tunnels each time
	// the agent registers.
	OnTunnel func(tunnels []protocol.TunnelAssignment)
}

// Agent maintains a tunnel session with the server, reconnecting
// whenever it is lost.
type Agent struct {
	logger   *slog.Logger
	cfg      config.AgentConfig
	onTunnel func(tunnels []protocol.TunnelAssignment)
}

func New(options Options) *Agent {
	return &Agent{
		logger:   options.Logger,
		cfg:      options.Cfg,
		onTunnel: options.OnTunnel,
	}
}

// RejectedError is returned when the server refuses to register the
// agent.
type RejectedError struct {
	Code   protocol.ErrorCode
	Reason string
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("server rejected registration (%s): %s", e.Code, e.Reason)
}

// Run keeps a tunnel session alive until ctx is cancelled, reconnecting
// with exponential backoff whenever the session fails or ends. It gives
// up and returns an error only when retrying cannot help, such as a bad
// token, an untrusted server certificate, or another agent taking over
// this agent's ID.
func (a *Agent) Run(ctx context.Context) error {
	tlsConfig, err := a.tlsConfig()
	if err != nil {
		return err
	}

	backoff := minBackoff

	for {
		established, err := a.runSession(ctx, tlsConfig)
		if ctx.Err() != nil {
			return nil
		}

		if isPermanent(err) {
			return err
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

// isPermanent reports whether err will recur on every retry.
func isPermanent(err error) bool {
	if rejected, ok := errors.AsType[*RejectedError](err); ok {
		return !rejected.Code.Retryable()
	}

	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}

	if _, ok := errors.AsType[*tlsutil.FingerprintMismatchError](err); ok {
		return true
	}

	return errors.Is(err, ErrReplaced)
}

// tlsConfig builds the client TLS configuration: a pinned fingerprint,
// a custom CA, or the system roots.
func (a *Agent) tlsConfig() (*tls.Config, error) {
	host, _, err := net.SplitHostPort(a.cfg.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid server address %q: %w", a.cfg.ServerAddress, err)
	}

	if a.cfg.Fingerprint != "" {
		cfg := tlsutil.PinnedConfig(a.cfg.Fingerprint)
		cfg.ServerName = host
		return cfg, nil
	}

	cfg := &tls.Config{ServerName: host}

	if a.cfg.TLSCAFile != "" {
		pem, err := os.ReadFile(a.cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("cannot read TLS CA file: %w", err)
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in TLS CA file %s", a.cfg.TLSCAFile)
		}
		cfg.RootCAs = pool
	}

	return cfg, nil
}

// runSession registers with the server and relays tunnel traffic until
// ctx is cancelled or the session ends. established reports whether the
// tunnels were set up before the session ended.
func (a *Agent) runSession(ctx context.Context, tlsConfig *tls.Config) (established bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	session, err := transport.Dial(dialCtx, a.cfg.ServerAddress, tlsConfig)
	cancel()
	if err != nil {
		// The usual cause is a server on its default self-signed
		// certificate with nothing configured to trust it.
		if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok && a.cfg.Fingerprint == "" && a.cfg.TLSCAFile == "" {
			return false, fmt.Errorf("cannot connect to server %s: %w (if the server uses its self-signed certificate, pass -fingerprint with the value from the server's startup log)", a.cfg.ServerAddress, err)
		}
		return false, fmt.Errorf("cannot connect to server %s: %w", a.cfg.ServerAddress, err)
	}
	defer session.Close(transport.CloseNormal, "")

	stop := context.AfterFunc(ctx, func() {
		session.Close(transport.CloseNormal, "agent shutting down")
	})
	defer stop()

	control, assignments, err := a.handshake(ctx, session)
	if err != nil {
		return false, err
	}
	defer control.Close()

	tunnels := make(map[uint32]config.TunnelConfig, len(assignments))
	hasUDP := false
	for _, assignment := range assignments {
		for _, tc := range a.cfg.Tunnels {
			if tc.Name == assignment.Name {
				tunnels[assignment.ID] = tc
				hasUDP = hasUDP || tc.Protocol == protocol.ProtocolUDP
				a.logger.Info("tunnel established", "tunnel", tc.Name, "protocol", tc.Protocol, "publicPort", assignment.PublicPort, "localAddr", tc.LocalAddress)
			}
		}
	}

	if a.onTunnel != nil {
		a.onTunnel(assignments)
	}

	if hasUDP {
		forwarder := newUDPForwarder(session, tunnels, a.logger)
		go forwarder.run()
	}

	// The control stream lives as long as the session. The server
	// closing it ends the session.
	go func() {
		io.Copy(io.Discard, control)
		session.Close(transport.CloseNormal, "control stream closed")
	}()

	for {
		stream, err := session.AcceptStream(context.Background())
		if err != nil {
			if code, ok := transport.PeerCloseCode(err); ok && code == transport.CloseReplaced {
				return true, ErrReplaced
			}
			return true, fmt.Errorf("tunnel session ended: %w", err)
		}

		go a.handleStream(stream, tunnels)
	}
}

// handshake opens the control stream, registers the agent's tunnels and
// returns the server's assignments.
func (a *Agent) handshake(ctx context.Context, session transport.Session) (net.Conn, []protocol.TunnelAssignment, error) {
	openCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	control, err := session.OpenStream(openCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot open control stream: %w", err)
	}

	control.SetDeadline(time.Now().Add(handshakeTimeout))
	defer control.SetDeadline(time.Time{})

	requests := make([]protocol.TunnelRequest, len(a.cfg.Tunnels))
	for i, tc := range a.cfg.Tunnels {
		requests[i] = protocol.TunnelRequest{Name: tc.Name, Protocol: tc.Protocol}
	}

	if err := protocol.WriteMessage(control, protocol.KindRegister, protocol.RegisterMessage{
		Version: protocol.Version,
		AgentID: a.cfg.AgentID,
		Name:    a.cfg.Name,
		Token:   a.cfg.Token,
		Tunnels: requests,
	}); err != nil {
		control.Close()
		return nil, nil, fmt.Errorf("failed to send register message: %w", err)
	}

	reply, err := protocol.ReadMessage(control)
	if err != nil {
		control.Close()
		return nil, nil, fmt.Errorf("failed to read handshake reply: %w", err)
	}

	switch msg := reply.(type) {
	case protocol.OkMessage:
		if len(msg.Tunnels) != len(requests) {
			control.Close()
			return nil, nil, fmt.Errorf("server assigned %d tunnels, requested %d", len(msg.Tunnels), len(requests))
		}
		return control, msg.Tunnels, nil
	case protocol.ErrorMessage:
		control.Close()
		return nil, nil, &RejectedError{Code: msg.Code, Reason: msg.Reason}
	default:
		control.Close()
		return nil, nil, errors.New("unexpected handshake reply")
	}
}
