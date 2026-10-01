// Package agent implements the client side of a tunnel: it registers
// with the server and relays each incoming stream to a local service.
package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/proxyproto"
	"github.com/prashunchitkr/khelamgg/internal/relay"
	"github.com/prashunchitkr/khelamgg/internal/transport"
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
// token or an untrusted server certificate.
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

	_, ok := errors.AsType[*tls.CertificateVerificationError](err)
	return ok
}

// tlsConfig builds the client TLS configuration, or returns nil when
// TLS is disabled.
func (a *Agent) tlsConfig() (*tls.Config, error) {
	if !a.cfg.TLS {
		return nil, nil
	}

	host, _, err := net.SplitHostPort(a.cfg.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("invalid server address %q: %w", a.cfg.ServerAddress, err)
	}

	cfg := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS13,
	}

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
// tunnel was set up before the session ended.
func (a *Agent) runSession(ctx context.Context, tlsConfig *tls.Config) (established bool, err error) {
	var conn net.Conn
	netDialer := &net.Dialer{Timeout: dialTimeout}
	if tlsConfig != nil {
		dialer := tls.Dialer{NetDialer: netDialer, Config: tlsConfig}
		conn, err = dialer.DialContext(ctx, "tcp", a.cfg.ServerAddress)
	} else {
		conn, err = netDialer.DialContext(ctx, "tcp", a.cfg.ServerAddress)
	}
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

	// After the OK reply the server multiplexes the connection, so the
	// agent must do the same.
	session, err := transport.Client(conn)
	if err != nil {
		return false, fmt.Errorf("cannot start session: %w", err)
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
		Version: protocol.Version,
		Name:    a.cfg.Name,
		Token:   a.cfg.Token,
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
		return 0, &RejectedError{Code: msg.Code, Reason: msg.Reason}
	default:
		return 0, errors.New("unexpected handshake reply")
	}
}

// handleStream reads the stream header and relays the tunnelled
// connection to the local service.
func (a *Agent) handleStream(stream net.Conn) {
	header, err := readStreamHeader(stream)
	if err != nil {
		a.logger.Error("invalid stream header", "error", err)
		stream.Close()
		return
	}

	localConn, err := net.DialTimeout("tcp", a.cfg.TunnelAddress, dialTimeout)
	if err != nil {
		a.logger.Error("failed to connect to local service", "tunnelAddr", a.cfg.TunnelAddress, "clientAddr", header.ClientAddr, "error", err)
		stream.Close()
		return
	}

	if a.cfg.ProxyProtocol {
		if err := writeProxyHeader(localConn, header); err != nil {
			a.logger.Error("failed to send PROXY header", "clientAddr", header.ClientAddr, "error", err)
			localConn.Close()
			stream.Close()
			return
		}
	}

	a.logger.Debug("stream opened", "clientAddr", header.ClientAddr)
	relay.Pipe(stream, localConn)
}

// readStreamHeader reads the STREAM_OPEN frame that precedes the
// tunnelled bytes on every stream.
func readStreamHeader(stream net.Conn) (protocol.StreamOpenMessage, error) {
	stream.SetReadDeadline(time.Now().Add(handshakeTimeout))
	defer stream.SetReadDeadline(time.Time{})

	msg, err := protocol.ReadMessage(stream)
	if err != nil {
		return protocol.StreamOpenMessage{}, err
	}

	header, ok := msg.(protocol.StreamOpenMessage)
	if !ok {
		return protocol.StreamOpenMessage{}, errors.New("expected stream open message")
	}
	return header, nil
}

func writeProxyHeader(localConn net.Conn, header protocol.StreamOpenMessage) error {
	client, err := netip.ParseAddrPort(header.ClientAddr)
	if err != nil {
		return fmt.Errorf("invalid client address: %w", err)
	}

	public, err := netip.ParseAddrPort(header.PublicAddr)
	if err != nil {
		return fmt.Errorf("invalid public address: %w", err)
	}

	return proxyproto.WriteV2(localConn, net.TCPAddrFromAddrPort(client), net.TCPAddrFromAddrPort(public))
}
