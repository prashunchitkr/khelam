package server

import (
	"net"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/relay"
	"github.com/prashunchitkr/khelamgg/internal/transport"
)

// bindConnection opens a stream to the agent, announces the public
// client on it, and copies traffic between the public connection and
// that stream.
func (s *Server) bindConnection(session transport.Session, publicConn net.Conn) {
	stream, err := session.Open()
	if err != nil {
		s.logger.Error("failed to open tunnel stream", "publicAddr", publicConn.RemoteAddr(), "error", err)
		publicConn.Close()
		return
	}

	if err := protocol.WriteMessage(stream, protocol.KindStreamOpen, protocol.StreamOpenMessage{
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
