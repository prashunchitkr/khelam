// Package relay copies traffic between two connections with correct
// half-close semantics.
package relay

import (
	"io"
	"net"
	"sync"
	"time"
)

// closeWriter is implemented by connections that support shutting down
// only their write side, such as *net.TCPConn.
type closeWriter interface {
	CloseWrite() error
}

// Pipe relays traffic between a and b until both directions have
// finished, then closes both.
//
// When one direction reaches a clean EOF, only the write side of the
// destination is shut down, so the peer sees EOF while the opposite
// direction keeps flowing. If a direction fails with an error, both
// connections are aborted so the other direction cannot block forever.
func Pipe(a, b net.Conn) {
	defer a.Close()
	defer b.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		copyHalf(a, b, a, b)
	}()

	go func() {
		defer wg.Done()
		copyHalf(b, a, a, b)
	}()

	wg.Wait()
}

func copyHalf(dst, src, a, b net.Conn) {
	if _, err := io.Copy(dst, src); err != nil {
		abort(a, b)
		return
	}
	closeWrite(dst)
}

// closeWrite signals EOF to the peer of conn without preventing further
// reads. yamux streams have no CloseWrite, but their Close is already a
// half-close: it sends FIN and leaves the stream readable.
func closeWrite(conn net.Conn) {
	if cw, ok := conn.(closeWriter); ok {
		cw.CloseWrite()
		return
	}
	conn.Close()
}

// abort unblocks any pending reads or writes on both connections.
func abort(a, b net.Conn) {
	now := time.Now()
	a.SetDeadline(now)
	b.SetDeadline(now)
}
