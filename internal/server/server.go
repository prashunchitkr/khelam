package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
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

	mu       sync.Mutex
	sessions map[*yamux.Session]struct{}
	wg       sync.WaitGroup
}

func New(options Options) *Server {
	return &Server{
		logger:   options.Logger,
		cfg:      options.Cfg,
		portPool: NewPortPool(options.Cfg.PortRangeStart, options.Cfg.PortRangeEnd),
		sessions: make(map[*yamux.Session]struct{}),
	}
}

// Start runs the server until ctx is cancelled, then shuts down all
// active agent tunnels before returning.
func (s *Server) Start(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.ControlAddress)
	if err != nil {
		return fmt.Errorf("error listening to agents: %w", err)
	}
	defer listener.Close()

	s.logger.Info("server accepting clients", "address", listener.Addr().String())

	go func() {
		<-ctx.Done()
		listener.Close()
		s.closeSessions()
	}()

	for {
		agentConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			s.logger.Error("failed to accept agent connection", "error", err)
			continue
		}

		s.wg.Add(1)
		go s.handleAgentConnection(agentConn)
	}

	s.wg.Wait()
	return nil
}

func (s *Server) closeSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for session := range s.sessions {
		session.Close()
	}
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
