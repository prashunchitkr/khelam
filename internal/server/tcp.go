package server

import (
	"context"
	"net"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/relay"
	"github.com/prashunchitkr/khelamgg/internal/transport"
)

// openStreamTimeout bounds waiting for the agent to allow another
// stream, which it does only while under its stream limit.
const openStreamTimeout = 5 * time.Second

// serveTCP accepts public connections for a TCP tunnel until its
// listener is closed.
func (s *Server) serveTCP(session transport.Session, t *tunnel) {
	listener := t.listener.(net.Listener)

	var slots chan struct{}
	if s.cfg.MaxConnsPerTunnel > 0 {
		slots = make(chan struct{}, s.cfg.MaxConnsPerTunnel)
	}

	for {
		publicConn, err := listener.Accept()
		if err != nil {
			return
		}

		if slots != nil {
			select {
			case slots <- struct{}{}:
			default:
				s.logger.Warn("tunnel connection limit reached", "tunnel", t.Name, "publicPort", t.PublicPort, "publicAddr", publicConn.RemoteAddr())
				publicConn.Close()
				continue
			}
		}

		go func() {
			s.bindConnection(session, t.ID, publicConn)
			if slots != nil {
				<-slots
			}
		}()
	}
}

// bindConnection opens a stream to the agent, announces the public
// client on it, and copies traffic between the public connection and
// that stream.
func (s *Server) bindConnection(session transport.Session, tunnelID uint32, publicConn net.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), openStreamTimeout)
	stream, err := session.OpenStream(ctx)
	cancel()
	if err != nil {
		s.logger.Error("failed to open tunnel stream", "publicAddr", publicConn.RemoteAddr(), "error", err)
		publicConn.Close()
		return
	}

	if err := protocol.WriteMessage(stream, protocol.KindStreamOpen, protocol.StreamOpenMessage{
		TunnelID:   tunnelID,
		ClientAddr: publicConn.RemoteAddr().String(),
		PublicAddr: publicConn.LocalAddr().String(),
	}); err != nil {
		s.logger.Error("failed to send stream header", "publicAddr", publicConn.RemoteAddr(), "error", err)
		stream.Close()
		publicConn.Close()
		return
	}

	relay.Pipe(publicConn, stream)
}
