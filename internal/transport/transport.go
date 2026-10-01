// Package transport carries tunnel traffic between the server and an
// agent over a single QUIC connection: reliable streams for TCP and
// unreliable datagrams for UDP. Callers depend only on Session, so the
// QUIC library stays an implementation detail.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

// ALPN identifies the khelam protocol during the TLS handshake.
const ALPN = "khelam/2"

const (
	keepAlivePeriod = 10 * time.Second
	maxIdleTimeout  = 30 * time.Second

	// maxIncomingStreams bounds concurrent streams a peer may open. The
	// server opens one per public TCP connection, so this must exceed
	// the server's per-agent connection limit.
	maxIncomingStreams = 1 << 16
)

// CloseCode tells the peer why a session was closed.
type CloseCode uint64

const (
	// CloseNormal is a plain shutdown with no further meaning.
	CloseNormal CloseCode = 0
	// CloseReplaced means another agent registered with the same ID.
	CloseReplaced CloseCode = 1
	// CloseShutdown means the server is shutting down.
	CloseShutdown CloseCode = 2
)

// Session is a multiplexed connection between the server and an agent.
type Session interface {
	// OpenStream starts a new reliable stream to the peer.
	OpenStream(ctx context.Context) (net.Conn, error)

	// AcceptStream waits for the next stream opened by the peer.
	AcceptStream(ctx context.Context) (net.Conn, error)

	// SendDatagram sends an unreliable message. Payloads larger than the
	// path allows fail with an error wrapping ErrDatagramTooLarge.
	SendDatagram(b []byte) error

	// ReceiveDatagram waits for the next datagram from the peer.
	ReceiveDatagram(ctx context.Context) ([]byte, error)

	// Close tears down the session and every stream on it, telling the
	// peer why.
	Close(code CloseCode, reason string) error

	// Done is closed once the session has ended.
	Done() <-chan struct{}

	RemoteAddr() net.Addr
}

// ErrDatagramTooLarge is returned by SendDatagram for oversized payloads.
var ErrDatagramTooLarge = errors.New("datagram too large")

// PeerCloseCode reports the code the peer gave when closing the session
// that produced err.
func PeerCloseCode(err error) (CloseCode, bool) {
	if appErr, ok := errors.AsType[*quic.ApplicationError](err); ok && appErr.Remote {
		return CloseCode(appErr.ErrorCode), true
	}
	return 0, false
}

func quicConfig() *quic.Config {
	return &quic.Config{
		KeepAlivePeriod:    keepAlivePeriod,
		MaxIdleTimeout:     maxIdleTimeout,
		MaxIncomingStreams: maxIncomingStreams,
		EnableDatagrams:    true,
	}
}

func withALPN(tlsConf *tls.Config) *tls.Config {
	tlsConf = tlsConf.Clone()
	tlsConf.NextProtos = []string{ALPN}
	tlsConf.MinVersion = tls.VersionTLS13
	return tlsConf
}

// Listener accepts sessions from agents.
type Listener struct {
	ln *quic.Listener
}

// Listen accepts sessions on pc. The caller keeps ownership of pc and
// must close it after closing the listener.
func Listen(pc net.PacketConn, tlsConf *tls.Config) (*Listener, error) {
	ln, err := quic.Listen(pc, withALPN(tlsConf), quicConfig())
	if err != nil {
		return nil, err
	}
	return &Listener{ln: ln}, nil
}

// Accept waits for the next session whose TLS handshake has completed.
func (l *Listener) Accept(ctx context.Context) (Session, error) {
	conn, err := l.ln.Accept(ctx)
	if err != nil {
		return nil, err
	}
	return &session{conn: conn}, nil
}

// Close stops accepting sessions. Sessions already accepted are not
// affected.
func (l *Listener) Close() error {
	return l.ln.Close()
}

// Dial connects to a server at addr.
func Dial(ctx context.Context, addr string, tlsConf *tls.Config) (Session, error) {
	conn, err := quic.DialAddr(ctx, addr, withALPN(tlsConf), quicConfig())
	if err != nil {
		return nil, err
	}
	return &session{conn: conn}, nil
}

type session struct {
	conn *quic.Conn
}

func (s *session) OpenStream(ctx context.Context) (net.Conn, error) {
	stream, err := s.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return s.wrap(stream), nil
}

func (s *session) AcceptStream(ctx context.Context) (net.Conn, error) {
	stream, err := s.conn.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return s.wrap(stream), nil
}

func (s *session) SendDatagram(b []byte) error {
	err := s.conn.SendDatagram(b)
	if _, ok := errors.AsType[*quic.DatagramTooLargeError](err); ok {
		return errors.Join(ErrDatagramTooLarge, err)
	}
	return err
}

func (s *session) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return s.conn.ReceiveDatagram(ctx)
}

func (s *session) Close(code CloseCode, reason string) error {
	return s.conn.CloseWithError(quic.ApplicationErrorCode(code), reason)
}

func (s *session) Done() <-chan struct{} {
	return s.conn.Context().Done()
}

func (s *session) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *session) wrap(stream *quic.Stream) net.Conn {
	return &streamConn{
		Stream: stream,
		local:  s.conn.LocalAddr(),
		remote: s.conn.RemoteAddr(),
	}
}

// streamConn adapts a QUIC stream to net.Conn.
type streamConn struct {
	*quic.Stream
	local, remote net.Addr
}

func (c *streamConn) LocalAddr() net.Addr  { return c.local }
func (c *streamConn) RemoteAddr() net.Addr { return c.remote }

// CloseWrite sends FIN while leaving the stream readable. QUIC's own
// Stream.Close only closes the send side, so it is exactly this.
func (c *streamConn) CloseWrite() error {
	return c.Stream.Close()
}

// Close closes both directions of the stream.
func (c *streamConn) Close() error {
	c.Stream.CancelRead(0)
	return c.Stream.Close()
}
