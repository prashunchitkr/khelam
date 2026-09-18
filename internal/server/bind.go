package server

import (
	"io"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
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

	copyBidirectional(publicConn, stream)
}

// copyBidirectional relays traffic between two connections until both
// directions have finished, then closes both.
func copyBidirectional(a, b net.Conn) {
	defer a.Close()
	defer b.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		io.Copy(a, b)
	}()

	go func() {
		defer wg.Done()
		io.Copy(b, a)
	}()

	wg.Wait()
}
