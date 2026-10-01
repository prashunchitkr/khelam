package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/prashunchitkr/khelamgg/internal/config"
)

// Options carries the dependencies required to construct a Server.
type Options struct {
	Logger *slog.Logger
	Cfg    config.ServerConfig
}

// Server accepts agent control connections, assigns each an exclusive
// public port, and tunnels inbound traffic back to the agent.
type Server struct {
	logger   *slog.Logger
	cfg      config.ServerConfig
	portPool *PortPool

	// handshakeSlots holds one token per connection still registering.
	handshakeSlots chan struct{}

	wg sync.WaitGroup
}

func New(options Options) *Server {
	return &Server{
		logger:   options.Logger,
		cfg:      options.Cfg,
		portPool: NewPortPool(options.Cfg.PortRangeStart, options.Cfg.PortRangeEnd),

		handshakeSlots: make(chan struct{}, maxPendingHandshakes),
	}
}

// Start listens on the configured control address and serves agents
// until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.ControlAddress)
	if err != nil {
		return fmt.Errorf("error listening to agents: %w", err)
	}

	return s.Serve(ctx, listener)
}

// Serve accepts agents on listener until ctx is cancelled, then shuts
// down all active agent tunnels before returning. Serve takes ownership
// of listener and wraps it in TLS when a certificate is configured.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	defer listener.Close()

	useTLS := s.cfg.TLSCertFile != ""
	if useTLS {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCertFile, s.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("cannot load TLS certificate: %w", err)
		}

		listener = tls.NewListener(listener, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		})
	}

	switch {
	case s.cfg.Token == "":
		s.logger.Warn("authentication disabled: any agent may register")
	case !useTLS:
		s.logger.Warn("TLS disabled: agent tokens are sent in plaintext")
	}

	s.logger.Info("server accepting clients", "address", listener.Addr().String(), "tls", useTLS)

	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()

	for {
		agentConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			s.logger.Error("failed to accept agent connection", "error", err)
			continue
		}

		select {
		case s.handshakeSlots <- struct{}{}:
		default:
			s.logger.Warn("too many pending handshakes, dropping agent connection", "agentAddr", agentConn.RemoteAddr())
			agentConn.Close()
			continue
		}

		s.wg.Add(1)
		go s.handleAgentConnection(ctx, agentConn)
	}

	s.wg.Wait()
	return nil
}

// allocatePublicListener takes ports from the pool until one can be
// bound. Ports that fail to bind (e.g. held by another process) go to
// the back of the pool, so a single bad port cannot block every
// registration.
func (s *Server) allocatePublicListener() (uint32, net.Listener, error) {
	attempts := s.portPool.Free()
	if attempts == 0 {
		return 0, nil, errors.New("no public ports available")
	}

	var lastErr error
	for range attempts {
		port, err := s.portPool.Allocate()
		if err != nil {
			return 0, nil, err
		}

		listener, err := openPortListener(port)
		if err == nil {
			return port, listener, nil
		}

		s.portPool.Release(port)
		s.logger.Warn("public port unavailable, trying next", "port", port, "error", err)
		lastErr = err
	}

	return 0, nil, fmt.Errorf("no bindable public port: %w", lastErr)
}

func openPortListener(port uint32) (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("cannot allocate port %d: %w", port, err)
	}
	return listener, nil
}
