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

const (
	portPoolStart = 41000
	portPoolEnd   = 41999
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
		portPool: NewPortPool(portPoolStart, portPoolEnd),
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

func openPortListener(port uint32) (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, fmt.Errorf("cannot allocate port %d: %w", port, err)
	}
	return listener, nil
}
