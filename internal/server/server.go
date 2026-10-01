package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/tlsutil"
	"github.com/prashunchitkr/khelamgg/internal/transport"
)

// Options carries the dependencies required to construct a Server.
type Options struct {
	Logger *slog.Logger
	Cfg    config.ServerConfig
}

// Server accepts agent sessions, assigns each requested tunnel a public
// port, and relays public traffic back to the agent.
type Server struct {
	logger       *slog.Logger
	cfg          config.ServerConfig
	cert         tls.Certificate
	pools        map[protocol.Protocol]*PortPool
	reservations *Reservations

	// handshakeSlots holds one token per session still registering.
	handshakeSlots chan struct{}

	mu     sync.Mutex
	agents map[string]*agentEntry

	wg sync.WaitGroup
}

// agentEntry marks an agent ID as connected. released is closed once
// that session has given back all of its ports.
type agentEntry struct {
	session  transport.Session
	released chan struct{}
}

// New loads the server certificate and port reservations.
func New(options Options) (*Server, error) {
	cfg := options.Cfg

	cert, err := loadCertificate(cfg)
	if err != nil {
		return nil, err
	}

	var reservationsPath string
	if cfg.StateDir != "" {
		reservationsPath = filepath.Join(cfg.StateDir, "ports.json")
	}
	reservations, err := LoadReservations(reservationsPath)
	if err != nil {
		return nil, err
	}

	return &Server{
		logger:       options.Logger,
		cfg:          cfg,
		cert:         cert,
		reservations: reservations,
		pools: map[protocol.Protocol]*PortPool{
			protocol.ProtocolTCP: NewPortPool(cfg.PortRangeStart, cfg.PortRangeEnd),
			protocol.ProtocolUDP: NewPortPool(cfg.PortRangeStart, cfg.PortRangeEnd),
		},
		handshakeSlots: make(chan struct{}, maxPendingHandshakes),
		agents:         make(map[string]*agentEntry),
	}, nil
}

// loadCertificate returns the configured certificate, or a self-signed
// one kept in the state directory (in memory if there is none).
func loadCertificate(cfg config.ServerConfig) (tls.Certificate, error) {
	switch {
	case cfg.TLSCertFile != "":
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("cannot load TLS certificate: %w", err)
		}
		return cert, nil
	case cfg.StateDir != "":
		cert, err := tlsutil.LoadOrCreateSelfSigned(
			filepath.Join(cfg.StateDir, "cert.pem"),
			filepath.Join(cfg.StateDir, "key.pem"),
		)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("cannot load self-signed certificate: %w", err)
		}
		return cert, nil
	default:
		return tlsutil.NewSelfSigned()
	}
}

// Fingerprint returns the SHA-256 fingerprint of the server certificate,
// for agents to pin with -fingerprint.
func (s *Server) Fingerprint() string {
	return tlsutil.Fingerprint(s.cert.Certificate[0])
}

// Start listens on the configured control address and serves agents
// until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	pc, err := net.ListenPacket("udp", s.cfg.ControlAddress)
	if err != nil {
		return fmt.Errorf("error listening to agents: %w", err)
	}

	return s.Serve(ctx, pc)
}

// Serve accepts agent sessions on pc until ctx is cancelled, then shuts
// down all active tunnels before returning. Serve takes ownership of pc.
func (s *Server) Serve(ctx context.Context, pc net.PacketConn) error {
	defer pc.Close()

	listener, err := transport.Listen(pc, &tls.Config{Certificates: []tls.Certificate{s.cert}})
	if err != nil {
		return fmt.Errorf("cannot start QUIC listener: %w", err)
	}
	defer listener.Close()

	if s.cfg.Token == "" {
		s.logger.Warn("authentication disabled: any agent may register")
	}
	if s.cfg.StateDir == "" {
		s.logger.Warn("no state directory: sticky ports and the self-signed certificate are lost on restart")
	}

	s.logger.Info("server accepting agents", "address", pc.LocalAddr().String(), "fingerprint", s.Fingerprint())

	for {
		session, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			s.logger.Error("failed to accept agent session", "error", err)
			continue
		}

		select {
		case s.handshakeSlots <- struct{}{}:
		default:
			s.logger.Warn("too many pending handshakes, dropping agent session", "agentAddr", session.RemoteAddr())
			session.Close(transport.CloseNormal, "server busy")
			continue
		}

		s.wg.Add(1)
		go s.handleSession(ctx, session)
	}

	s.wg.Wait()
	return nil
}

// claimAgent registers session as the live connection for agentID. If
// another session holds the ID, it is closed and its ports released
// first, so a reconnecting agent can reclaim its sticky ports.
func (s *Server) claimAgent(agentID string, session transport.Session) (*agentEntry, error) {
	entry := &agentEntry{session: session, released: make(chan struct{})}

	for {
		s.mu.Lock()
		prev := s.agents[agentID]
		if prev == nil {
			s.agents[agentID] = entry
			s.mu.Unlock()
			return entry, nil
		}
		s.mu.Unlock()

		s.logger.Info("replacing existing session for agent", "agentID", agentID, "oldAddr", prev.session.RemoteAddr(), "newAddr", session.RemoteAddr())
		prev.session.Close(transport.CloseReplaced, "another agent connected with the same ID")

		select {
		case <-prev.released:
		case <-time.After(handshakeTimeout):
			return nil, errors.New("previous session did not shut down")
		}
	}
}

// releaseAgent removes entry from the live agents once its ports are
// back in the pools.
func (s *Server) releaseAgent(agentID string, entry *agentEntry) {
	s.mu.Lock()
	if s.agents[agentID] == entry {
		delete(s.agents, agentID)
	}
	s.mu.Unlock()

	close(entry.released)
}

// allocatePort finds a public port for the tunnel identified by key and
// binds it. It prefers the tunnel's sticky port, then ports nobody has
// reserved, and only then takes over the port of an offline agent, so
// stickiness never costs capacity.
func (s *Server) allocatePort(key reservationKey) (uint32, io.Closer, error) {
	pool := s.pools[key.Protocol]

	if port, ok := s.reservations.Lookup(key); ok && pool.Take(port) {
		listener, err := bindPublic(key.Protocol, port)
		if err == nil {
			return port, listener, nil
		}
		pool.Release(port)
		s.logger.Warn("sticky port unavailable, assigning a new one", "port", port, "protocol", key.Protocol, "error", err)
	}

	skips := []func(uint32) bool{
		func(port uint32) bool { return s.reservations.ReservedByOther(key, port) },
		nil,
	}

	var lastErr error = errNoPorts
	for _, skip := range skips {
		// Ports that fail to bind (e.g. held by another process) go to
		// the back of the pool, so a single bad port cannot block every
		// registration.
		for range pool.Free() {
			port, err := pool.Allocate(skip)
			if err != nil {
				break
			}

			listener, err := bindPublic(key.Protocol, port)
			if err != nil {
				pool.Release(port)
				s.logger.Warn("public port unavailable, trying next", "port", port, "protocol", key.Protocol, "error", err)
				lastErr = err
				continue
			}

			if err := s.reservations.Reserve(key, port); err != nil {
				s.logger.Error("failed to save port reservation", "error", err)
			}
			return port, listener, nil
		}
	}

	return 0, nil, fmt.Errorf("no bindable public port: %w", lastErr)
}

// bindPublic opens the public listener for a tunnel: a net.Listener for
// TCP or a *net.UDPConn for UDP.
func bindPublic(proto protocol.Protocol, port uint32) (io.Closer, error) {
	addr := fmt.Sprintf("0.0.0.0:%d", port)

	switch proto {
	case protocol.ProtocolTCP:
		return net.Listen("tcp", addr)
	case protocol.ProtocolUDP:
		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		return net.ListenUDP("udp", udpAddr)
	default:
		return nil, fmt.Errorf("unsupported protocol %q", proto)
	}
}
