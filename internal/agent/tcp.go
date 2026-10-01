package agent

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/prashunchitkr/khelam/internal/config"
	"github.com/prashunchitkr/khelam/internal/protocol"
	"github.com/prashunchitkr/khelam/internal/proxyproto"
	"github.com/prashunchitkr/khelam/internal/relay"
)

// handleStream reads the stream header and relays the tunnelled
// connection to the tunnel's local service.
func (a *Agent) handleStream(stream net.Conn, tunnels map[uint32]config.TunnelConfig) {
	header, err := readStreamHeader(stream)
	if err != nil {
		a.logger.Error("invalid stream header", "error", err)
		stream.Close()
		return
	}

	tc, ok := tunnels[header.TunnelID]
	if !ok || tc.Protocol != protocol.ProtocolTCP {
		a.logger.Error("stream for unknown TCP tunnel", "tunnelID", header.TunnelID)
		stream.Close()
		return
	}

	localConn, err := net.DialTimeout("tcp", tc.LocalAddress, dialTimeout)
	if err != nil {
		a.logger.Error("failed to connect to local service", "tunnel", tc.Name, "localAddr", tc.LocalAddress, "clientAddr", header.ClientAddr, "error", err)
		stream.Close()
		return
	}

	if a.cfg.ProxyProtocol {
		if err := writeProxyHeader(localConn, header); err != nil {
			a.logger.Error("failed to send PROXY header", "clientAddr", header.ClientAddr, "error", err)
			localConn.Close()
			stream.Close()
			return
		}
	}

	a.logger.Debug("stream opened", "tunnel", tc.Name, "clientAddr", header.ClientAddr)
	relay.Pipe(stream, localConn)
}

// readStreamHeader reads the STREAM_OPEN frame that precedes the
// tunnelled bytes on every stream.
func readStreamHeader(stream net.Conn) (protocol.StreamOpenMessage, error) {
	stream.SetReadDeadline(time.Now().Add(handshakeTimeout))
	defer stream.SetReadDeadline(time.Time{})

	msg, err := protocol.ReadMessage(stream)
	if err != nil {
		return protocol.StreamOpenMessage{}, err
	}

	header, ok := msg.(protocol.StreamOpenMessage)
	if !ok {
		return protocol.StreamOpenMessage{}, errors.New("expected stream open message")
	}
	return header, nil
}

func writeProxyHeader(localConn net.Conn, header protocol.StreamOpenMessage) error {
	client, err := netip.ParseAddrPort(header.ClientAddr)
	if err != nil {
		return fmt.Errorf("invalid client address: %w", err)
	}

	public, err := netip.ParseAddrPort(header.PublicAddr)
	if err != nil {
		return fmt.Errorf("invalid public address: %w", err)
	}

	return proxyproto.WriteV2(localConn, net.TCPAddrFromAddrPort(client), net.TCPAddrFromAddrPort(public))
}
