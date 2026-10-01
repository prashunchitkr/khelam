package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
	"github.com/prashunchitkr/khelamgg/internal/transport"
)

const (
	udpSweepInterval = 10 * time.Second
	maxUDPPacketSize = 64 * 1024
)

// udpTunnel relays datagrams between public UDP clients and the agent.
// Each client address is a flow, identified to the agent by a flow ID.
type udpTunnel struct {
	id       uint32
	name     string
	conn     *net.UDPConn
	session  transport.Session
	logger   *slog.Logger
	maxFlows int

	mu     sync.Mutex
	byAddr map[netip.AddrPort]*udpFlow
	byID   map[uint32]*udpFlow
	nextID uint32
}

type udpFlow struct {
	id   uint32
	addr netip.AddrPort
	// lastSeen is the Unix nanosecond time of the latest packet in
	// either direction.
	lastSeen atomic.Int64
}

func (f *udpFlow) touch() { f.lastSeen.Store(time.Now().UnixNano()) }

func (s *Server) newUDPTunnel(session transport.Session, t *tunnel) *udpTunnel {
	return &udpTunnel{
		id:       t.ID,
		name:     t.Name,
		conn:     t.listener.(*net.UDPConn),
		session:  session,
		logger:   s.logger,
		maxFlows: s.cfg.MaxConnsPerTunnel,
		byAddr:   make(map[netip.AddrPort]*udpFlow),
		byID:     make(map[uint32]*udpFlow),
	}
}

// serve forwards packets from public clients to the agent until the
// public socket is closed.
func (u *udpTunnel) serve() {
	done := make(chan struct{})
	defer close(done)
	go u.sweepLoop(done)

	buf := make([]byte, maxUDPPacketSize)
	for {
		n, addr, err := u.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}

		flow := u.flowFor(netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port()))
		if flow == nil {
			continue
		}
		flow.touch()

		datagram := protocol.AppendDatagram(make([]byte, 0, protocol.DatagramHeaderSize+n), u.id, flow.id, buf[:n])
		if err := u.session.SendDatagram(datagram); err != nil {
			if errors.Is(err, transport.ErrDatagramTooLarge) {
				u.logger.Debug("dropping oversized UDP packet", "tunnel", u.name, "size", n)
				continue
			}
			return
		}
	}
}

// flowFor returns the flow for a client address, creating it if there
// is room. It returns nil when the tunnel is at its flow limit.
func (u *udpTunnel) flowFor(addr netip.AddrPort) *udpFlow {
	u.mu.Lock()
	defer u.mu.Unlock()

	if flow, ok := u.byAddr[addr]; ok {
		return flow
	}

	if u.maxFlows > 0 && len(u.byAddr) >= u.maxFlows {
		u.logger.Warn("tunnel UDP flow limit reached", "tunnel", u.name, "clientAddr", addr)
		return nil
	}

	u.nextID++
	flow := &udpFlow{id: u.nextID, addr: addr}
	u.byAddr[addr] = flow
	u.byID[flow.id] = flow
	return flow
}

// deliver sends a reply from the agent to the flow's public client.
func (u *udpTunnel) deliver(flowID uint32, payload []byte) {
	u.mu.Lock()
	flow, ok := u.byID[flowID]
	u.mu.Unlock()
	if !ok {
		return
	}

	flow.touch()
	u.conn.WriteToUDPAddrPort(payload, flow.addr)
}

func (u *udpTunnel) sweepLoop(done <-chan struct{}) {
	ticker := time.NewTicker(udpSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			u.sweep(now)
		}
	}
}

// sweep forgets flows that have been idle longer than the timeout.
func (u *udpTunnel) sweep(now time.Time) {
	cutoff := now.Add(-protocol.UDPFlowIdleTimeout).UnixNano()

	u.mu.Lock()
	defer u.mu.Unlock()

	for addr, flow := range u.byAddr {
		if flow.lastSeen.Load() < cutoff {
			delete(u.byAddr, addr)
			delete(u.byID, flow.id)
		}
	}
}

// receiveDatagrams routes datagrams from the agent to the UDP tunnel
// they belong to until the session ends.
func (s *Server) receiveDatagrams(session transport.Session, tunnels map[uint32]*udpTunnel) {
	for {
		datagram, err := session.ReceiveDatagram(context.Background())
		if err != nil {
			return
		}

		tunnelID, flowID, payload, err := protocol.ParseDatagram(datagram)
		if err != nil {
			continue
		}

		if u, ok := tunnels[tunnelID]; ok {
			u.deliver(flowID, payload)
		}
	}
}
