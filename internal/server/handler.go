package server

import (
	"net"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/prashunchitkr/khelamgg/internal/protocol"
)

const handshakeTimeout = 5 * time.Second

// handleAgentConnection owns the full lifecycle of a single agent
// connection: handshake, port allocation, yamux session setup,
// public accept loop, and teardown.
func (s *Server) handleAgentConnection(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(handshakeTimeout))
	payload, err := protocol.ReadMessage(conn)
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		s.logger.Error("failed to read handshake message", "agentAddr", conn.RemoteAddr(), "error", err)
		return
	}

	register, ok := payload.(protocol.RegisterMessage)
	if !ok {
		s.logger.Error("unexpected handshake message", "agentAddr", conn.RemoteAddr())
		protocol.WriteMessage(conn, protocol.KindError, protocol.ErrorMessage{
			Reason: "expected register message",
		})
		return
	}

	s.logger.Info("agent connected", "agentName", register.Name, "agentAddr", conn.RemoteAddr())

	port, publicListener, err := s.allocatePublicListener()
	if err != nil {
		s.logger.Error("error allocating port", "agentName", register.Name, "agentAddr", conn.RemoteAddr(), "error", err)
		protocol.WriteMessage(conn, protocol.KindError, protocol.ErrorMessage{
			Reason: "error allocating port",
		})
		return
	}

	// The OK reply travels over the raw connection before yamux takes
	// it over, so the agent learns its public port before multiplexing
	// begins.
	if err := protocol.WriteMessage(conn, protocol.KindOk, protocol.OkMessage{
		Name:       register.Name,
		PublicPort: port,
	}); err != nil {
		s.logger.Error("failed to send port assignment", "agentName", register.Name, "agentAddr", conn.RemoteAddr(), "error", err)
		publicListener.Close()
		s.portPool.Release(port)
		return
	}

	yamuxConfig := yamux.DefaultConfig()
	yamuxConfig.EnableKeepAlive = true
	yamuxConfig.KeepAliveInterval = 30 * time.Second

	session, err := yamux.Server(conn, yamuxConfig)
	if err != nil {
		s.logger.Error("cannot start yamux session", "agentName", register.Name, "agentAddr", conn.RemoteAddr(), "error", err)
		publicListener.Close()
		s.portPool.Release(port)
		return
	}
	defer session.Close()

	defer func() {
		publicListener.Close()
		s.portPool.Release(port)
	}()

	if !s.trackSession(session) {
		return
	}
	defer s.untrackSession(session)

	s.logger.Info("tunnel allocated", "agentName", register.Name, "publicPort", port)

	go func() {
		<-session.CloseChan()
		publicListener.Close()
	}()

	for {
		publicConn, err := publicListener.Accept()
		if err != nil {
			return
		}

		go s.bindConnection(session, publicConn)
	}
}
