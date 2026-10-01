package server

import (
	"net"

	"github.com/hashicorp/yamux"
	"github.com/prashunchitkr/khelamgg/internal/relay"
)

// bindConnection opens a yamux stream to the agent and copies traffic
// between the public connection and that stream.
func (s *Server) bindConnection(session *yamux.Session, publicConn net.Conn) {
	stream, err := session.Open()
	if err != nil {
		s.logger.Error("failed to open tunnel stream", "publicAddr", publicConn.RemoteAddr(), "error", err)
		publicConn.Close()
		return
	}

	relay.Pipe(publicConn, stream)
}
