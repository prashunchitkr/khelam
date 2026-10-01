package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/transport"
)

const (
	handshakeTimeout = 5 * time.Second

	// maxPendingHandshakes caps sessions that have not finished
	// registering, so idle or slow clients cannot pile up.
	maxPendingHandshakes = 64

	maxTunnelsPerAgent = 16
	maxNameLength      = 64

	// rejectLinger is how long a rejected session stays open so the
	// agent can read the ERROR reply before the connection is closed.
	rejectLinger = time.Second
)

// tunnel is one public port opened for an agent.
type tunnel struct {
	protocol.TunnelAssignment
	key      reservationKey
	listener io.Closer
}

// handleSession owns the full lifecycle of a single agent session:
// registration, tunnel setup, relaying, and teardown.
func (s *Server) handleSession(ctx context.Context, session transport.Session) {
	defer s.wg.Done()
	defer session.Close(transport.CloseNormal, "")

	stop := context.AfterFunc(ctx, func() {
		session.Close(transport.CloseShutdown, "server shutting down")
	})
	defer stop()

	control, register, entry, tunnels, ok := s.register(ctx, session)
	if !ok {
		return
	}
	defer s.releaseAgent(register.AgentID, entry)

	s.logger.Info("agent registered", "agentName", register.Name, "agentID", register.AgentID, "agentAddr", session.RemoteAddr(), "tunnels", len(tunnels))

	var wg sync.WaitGroup
	udpTunnels := make(map[uint32]*udpTunnel)

	for _, t := range tunnels {
		s.logger.Info("tunnel open", "agentName", register.Name, "tunnel", t.Name, "protocol", t.Protocol, "publicPort", t.PublicPort)

		switch t.Protocol {
		case protocol.ProtocolTCP:
			wg.Go(func() { s.serveTCP(session, t) })
		case protocol.ProtocolUDP:
			u := s.newUDPTunnel(session, t)
			udpTunnels[t.ID] = u
			wg.Go(u.serve)
		}
	}

	if len(udpTunnels) > 0 {
		wg.Go(func() { s.receiveDatagrams(session, udpTunnels) })
	}

	// The control stream lives as long as the session. The agent closing
	// it ends the session.
	wg.Go(func() {
		io.Copy(io.Discard, control)
		session.Close(transport.CloseNormal, "control stream closed")
	})

	<-session.Done()
	s.logger.Info("agent disconnected", "agentName", register.Name, "agentID", register.AgentID)

	// Closing the public listeners ends the tunnel loops.
	s.closeTunnels(tunnels)
	wg.Wait()
}

// register runs the handshake on a new session: it reads REGISTER from
// the control stream the agent opens, validates it, claims the agent ID,
// opens every requested tunnel and replies with OK. On failure it sends
// an ERROR reply when possible and returns false.
func (s *Server) register(ctx context.Context, session transport.Session) (net.Conn, protocol.RegisterMessage, *agentEntry, []*tunnel, bool) {
	defer func() { <-s.handshakeSlots }()

	fail := func() (net.Conn, protocol.RegisterMessage, *agentEntry, []*tunnel, bool) {
		return nil, protocol.RegisterMessage{}, nil, nil, false
	}

	acceptCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	control, err := session.AcceptStream(acceptCtx)
	if err != nil {
		s.logger.Error("agent did not open a control stream", "agentAddr", session.RemoteAddr(), "error", err)
		return fail()
	}

	control.SetDeadline(time.Now().Add(handshakeTimeout))
	defer control.SetDeadline(time.Time{})

	msg, err := protocol.ReadMessage(control)
	if err != nil {
		s.logger.Error("failed to read handshake message", "agentAddr", session.RemoteAddr(), "error", err)
		return fail()
	}

	register, ok := msg.(protocol.RegisterMessage)
	if !ok {
		s.reject(session, control, protocol.ErrBadRequest, "expected register message")
		return fail()
	}

	if register.Version != protocol.Version {
		s.reject(session, control, protocol.ErrUnsupportedVersion, fmt.Sprintf("server speaks protocol version %d, agent sent %d", protocol.Version, register.Version))
		return fail()
	}

	principal, ok := s.authenticate(register.Token)
	if !ok {
		s.reject(session, control, protocol.ErrUnauthorized, "invalid token")
		return fail()
	}

	if err := validateRegister(register); err != nil {
		s.reject(session, control, protocol.ErrBadRequest, err.Error())
		return fail()
	}

	// Scope the agent ID to its token, so holders of one token can
	// neither take over nor reclaim the ports of another token's agents.
	// From here on AgentID is the scoped form.
	if principal != "" {
		register.AgentID = principal + "/" + register.AgentID
	}

	entry, err := s.claimAgent(register.AgentID, principal, session)
	if err != nil {
		s.reject(session, control, protocol.ErrNoCapacity, err.Error())
		return fail()
	}

	tunnels, err := s.openTunnels(register)
	if err != nil {
		s.releaseAgent(register.AgentID, entry)
		s.logger.Error("error allocating ports", "agentName", register.Name, "agentAddr", session.RemoteAddr(), "error", err)
		s.reject(session, control, protocol.ErrNoCapacity, "no public port available")
		return fail()
	}

	assignments := make([]protocol.TunnelAssignment, len(tunnels))
	for i, t := range tunnels {
		assignments[i] = t.TunnelAssignment
	}

	if err := protocol.WriteMessage(control, protocol.KindOk, protocol.OkMessage{Tunnels: assignments}); err != nil {
		s.logger.Error("failed to send tunnel assignments", "agentName", register.Name, "agentAddr", session.RemoteAddr(), "error", err)
		s.closeTunnels(tunnels)
		s.releaseAgent(register.AgentID, entry)
		return fail()
	}

	return control, register, entry, tunnels, true
}

func validateRegister(register protocol.RegisterMessage) error {
	if register.AgentID == "" || len(register.AgentID) > maxNameLength {
		return fmt.Errorf("agent ID must be 1-%d characters", maxNameLength)
	}

	if len(register.Name) > maxNameLength {
		return fmt.Errorf("agent name must be at most %d characters", maxNameLength)
	}

	if len(register.Tunnels) == 0 || len(register.Tunnels) > maxTunnelsPerAgent {
		return fmt.Errorf("request 1-%d tunnels", maxTunnelsPerAgent)
	}

	names := make(map[string]bool, len(register.Tunnels))
	for _, t := range register.Tunnels {
		if t.Name == "" || len(t.Name) > maxNameLength {
			return fmt.Errorf("tunnel name must be 1-%d characters", maxNameLength)
		}
		if names[t.Name] {
			return fmt.Errorf("duplicate tunnel name %q", t.Name)
		}
		names[t.Name] = true

		if t.Protocol != protocol.ProtocolTCP && t.Protocol != protocol.ProtocolUDP {
			return fmt.Errorf("tunnel %q: unsupported protocol %q", t.Name, t.Protocol)
		}
	}

	return nil
}

// openTunnels allocates and binds a public port for every requested
// tunnel, or none if any fails. Tunnel IDs follow request order from 1.
func (s *Server) openTunnels(register protocol.RegisterMessage) ([]*tunnel, error) {
	tunnels := make([]*tunnel, 0, len(register.Tunnels))

	for i, req := range register.Tunnels {
		key := reservationKey{AgentID: register.AgentID, Tunnel: req.Name, Protocol: req.Protocol}

		port, listener, err := s.allocatePort(key)
		if err != nil {
			s.closeTunnels(tunnels)
			return nil, fmt.Errorf("tunnel %q: %w", req.Name, err)
		}

		tunnels = append(tunnels, &tunnel{
			TunnelAssignment: protocol.TunnelAssignment{
				ID:         uint32(i + 1),
				Name:       req.Name,
				Protocol:   req.Protocol,
				PublicPort: port,
			},
			key:      key,
			listener: listener,
		})
	}

	return tunnels, nil
}

// closeTunnels closes the public listeners and returns their ports to
// the pools, noting when each sticky port was last used.
func (s *Server) closeTunnels(tunnels []*tunnel) {
	for _, t := range tunnels {
		t.listener.Close()
		s.pools[t.Protocol].Release(t.PublicPort)

		if err := s.reservations.Touch(t.key); err != nil {
			s.logger.Error("failed to save port reservation", "error", err)
		}
	}
}

// authenticate checks token against the tokens file, or else the
// shared secret, and returns the name of the matching token (empty for
// the shared secret or when authentication is disabled).
func (s *Server) authenticate(token string) (principal string, ok bool) {
	if s.tokens != nil {
		return s.tokens.Authenticate(token)
	}

	if s.cfg.Token == "" {
		return "", true
	}

	// Both sides are hashed first so the comparison is constant time
	// regardless of length.
	got := sha256.Sum256([]byte(token))
	want := sha256.Sum256([]byte(s.cfg.Token))
	return "", subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// reject sends an ERROR reply and gives the agent a moment to read it,
// since closing the session discards undelivered stream data.
func (s *Server) reject(session transport.Session, control net.Conn, code protocol.ErrorCode, reason string) {
	s.logger.Warn("agent rejected", "agentAddr", session.RemoteAddr(), "code", code, "reason", reason)

	err := protocol.WriteMessage(control, protocol.KindError, protocol.ErrorMessage{
		Code:   code,
		Reason: reason,
	})
	if err != nil {
		return
	}

	if cw, ok := control.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}

	select {
	case <-session.Done():
	case <-time.After(rejectLinger):
	}
}
