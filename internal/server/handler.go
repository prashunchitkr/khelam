package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/transport"
)

const (
	handshakeTimeout = 5 * time.Second

	// maxPendingHandshakes caps connections that have not finished
	// registering, so idle or slow clients cannot pile up.
	maxPendingHandshakes = 64
)

// handleAgentConnection owns the full lifecycle of a single agent
// connection: handshake, port allocation, session setup, public accept
// loop, and teardown.
func (s *Server) handleAgentConnection(ctx context.Context, conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	// Closing the connection on shutdown aborts a pending handshake and
	// tears down the session built on top of it.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	register, port, publicListener, ok := s.register(conn)
	if !ok {
		return
	}

	defer func() {
		publicListener.Close()
		s.portPool.Release(port)
	}()

	session, err := transport.Server(conn)
	if err != nil {
		s.logger.Error("cannot start session", "agentName", register.Name, "agentAddr", conn.RemoteAddr(), "error", err)
		return
	}
	defer session.Close()

	s.logger.Info("tunnel allocated", "agentName", register.Name, "publicPort", port)

	go func() {
		<-session.CloseChan()
		publicListener.Close()
	}()

	var slots chan struct{}
	if s.cfg.MaxConnsPerTunnel > 0 {
		slots = make(chan struct{}, s.cfg.MaxConnsPerTunnel)
	}

	for {
		publicConn, err := publicListener.Accept()
		if err != nil {
			return
		}

		if slots != nil {
			select {
			case slots <- struct{}{}:
			default:
				s.logger.Warn("tunnel connection limit reached", "agentName", register.Name, "publicPort", port, "publicAddr", publicConn.RemoteAddr())
				publicConn.Close()
				continue
			}
		}

		go func() {
			s.bindConnection(session, publicConn)
			if slots != nil {
				<-slots
			}
		}()
	}
}

// register runs the handshake on a new agent connection: it validates
// the REGISTER message, allocates a public port and replies with OK.
// On failure it sends an ERROR reply when possible and returns false.
// The OK reply travels over the raw connection before the session takes
// it over, so the agent learns its public port before multiplexing
// begins.
func (s *Server) register(conn net.Conn) (protocol.RegisterMessage, uint32, net.Listener, bool) {
	defer func() { <-s.handshakeSlots }()

	conn.SetDeadline(time.Now().Add(handshakeTimeout))
	defer conn.SetDeadline(time.Time{})

	msg, err := protocol.ReadMessage(conn)
	if err != nil {
		s.logger.Error("failed to read handshake message", "agentAddr", conn.RemoteAddr(), "error", err)
		return protocol.RegisterMessage{}, 0, nil, false
	}

	register, ok := msg.(protocol.RegisterMessage)
	if !ok {
		s.reject(conn, protocol.ErrBadRequest, "expected register message")
		return protocol.RegisterMessage{}, 0, nil, false
	}

	if register.Version != protocol.Version {
		s.reject(conn, protocol.ErrUnsupportedVersion, fmt.Sprintf("server speaks protocol version %d, agent sent %d", protocol.Version, register.Version))
		return protocol.RegisterMessage{}, 0, nil, false
	}

	if !s.authorized(register.Token) {
		s.reject(conn, protocol.ErrUnauthorized, "invalid token")
		return protocol.RegisterMessage{}, 0, nil, false
	}

	s.logger.Info("agent connected", "agentName", register.Name, "agentAddr", conn.RemoteAddr())

	port, publicListener, err := s.allocatePublicListener()
	if err != nil {
		s.logger.Error("error allocating port", "agentName", register.Name, "agentAddr", conn.RemoteAddr(), "error", err)
		s.reject(conn, protocol.ErrNoCapacity, "no public port available")
		return protocol.RegisterMessage{}, 0, nil, false
	}

	if err := protocol.WriteMessage(conn, protocol.KindOk, protocol.OkMessage{
		Name:       register.Name,
		PublicPort: port,
	}); err != nil {
		s.logger.Error("failed to send port assignment", "agentName", register.Name, "agentAddr", conn.RemoteAddr(), "error", err)
		publicListener.Close()
		s.portPool.Release(port)
		return protocol.RegisterMessage{}, 0, nil, false
	}

	return register, port, publicListener, true
}

// authorized reports whether token matches the configured secret.
// Both sides are hashed first so the comparison is constant time
// regardless of length.
func (s *Server) authorized(token string) bool {
	if s.cfg.Token == "" {
		return true
	}

	got := sha256.Sum256([]byte(token))
	want := sha256.Sum256([]byte(s.cfg.Token))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func (s *Server) reject(conn net.Conn, code protocol.ErrorCode, reason string) {
	s.logger.Warn("agent rejected", "agentAddr", conn.RemoteAddr(), "code", code, "reason", reason)
	protocol.WriteMessage(conn, protocol.KindError, protocol.ErrorMessage{
		Code:   code,
		Reason: reason,
	})
}
