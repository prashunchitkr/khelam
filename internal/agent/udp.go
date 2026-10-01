package agent

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prashunchitkr/khelam/internal/protocol"
	"github.com/prashunchitkr/khelam/internal/transport"
)

const (
	udpSweepInterval = 10 * time.Second
	maxUDPPacketSize = 64 * 1024
)

// udpForwarder relays datagrams between the server and local UDP
// services. Each public client (flow) gets its own local socket, so the
// service sees distinct clients and replies reach the right one.
type udpForwarder struct {
	session transport.Session
	tunnels map[uint32]route
	logger  *slog.Logger

	mu    sync.Mutex
	flows map[flowKey]*udpFlow
}

type flowKey struct {
	tunnelID, flowID uint32
}

type udpFlow struct {
	conn  net.Conn
	stats *tunnelStats
	// lastSeen is the Unix nanosecond time of the latest packet in
	// either direction.
	lastSeen atomic.Int64
}

func (f *udpFlow) touch() { f.lastSeen.Store(time.Now().UnixNano()) }

func newUDPForwarder(session transport.Session, tunnels map[uint32]route, logger *slog.Logger) *udpForwarder {
	return &udpForwarder{
		session: session,
		tunnels: tunnels,
		logger:  logger,
		flows:   make(map[flowKey]*udpFlow),
	}
}

// run forwards datagrams from the server to local services until the
// session ends, then closes every flow.
func (f *udpForwarder) run() {
	done := make(chan struct{})
	defer close(done)
	defer f.closeAll()
	go f.sweepLoop(done)

	for {
		datagram, err := f.session.ReceiveDatagram(context.Background())
		if err != nil {
			return
		}

		tunnelID, flowID, payload, err := protocol.ParseDatagram(datagram)
		if err != nil {
			continue
		}

		flow := f.flowFor(flowKey{tunnelID, flowID})
		if flow == nil {
			continue
		}

		flow.touch()
		flow.stats.bytesIn.Add(int64(len(payload)))
		flow.conn.Write(payload)
	}
}

// flowFor returns the local socket for a flow, dialing the tunnel's
// local service for a new one. It returns nil for unknown tunnels or if
// the local address cannot be dialed.
func (f *udpForwarder) flowFor(key flowKey) *udpFlow {
	f.mu.Lock()
	defer f.mu.Unlock()

	if flow, ok := f.flows[key]; ok {
		return flow
	}

	tc, ok := f.tunnels[key.tunnelID]
	if !ok || tc.Protocol != protocol.ProtocolUDP {
		return nil
	}

	conn, err := net.Dial("udp", tc.LocalAddress)
	if err != nil {
		f.logger.Error("failed to reach local UDP service", "tunnel", tc.Name, "localAddr", tc.LocalAddress, "error", err)
		return nil
	}

	flow := &udpFlow{conn: conn, stats: tc.stats}
	f.flows[key] = flow
	tc.stats.active.Add(1)
	tc.stats.total.Add(1)
	f.logger.Debug("UDP flow opened", "tunnel", tc.Name, "flowID", key.flowID)
	go f.readReplies(key, flow)

	return flow
}

// readReplies sends the local service's replies for one flow back to
// the server until the flow's socket is closed.
func (f *udpForwarder) readReplies(key flowKey, flow *udpFlow) {
	defer f.remove(key, flow)

	buf := make([]byte, maxUDPPacketSize)
	for {
		n, err := flow.conn.Read(buf)
		if err != nil {
			return
		}
		flow.touch()
		flow.stats.bytesOut.Add(int64(n))

		datagram := protocol.AppendDatagram(make([]byte, 0, protocol.DatagramHeaderSize+n), key.tunnelID, key.flowID, buf[:n])
		if err := f.session.SendDatagram(datagram); err != nil {
			if errors.Is(err, transport.ErrDatagramTooLarge) {
				f.logger.Debug("dropping oversized UDP reply", "size", n)
				continue
			}
			return
		}
	}
}

// remove closes a flow. It runs once per flow, when its reader exits.
func (f *udpForwarder) remove(key flowKey, flow *udpFlow) {
	flow.conn.Close()
	flow.stats.active.Add(-1)

	f.mu.Lock()
	defer f.mu.Unlock()

	if f.flows[key] == flow {
		delete(f.flows, key)
	}
}

func (f *udpForwarder) sweepLoop(done <-chan struct{}) {
	ticker := time.NewTicker(udpSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			f.sweep(now)
		}
	}
}

// sweep closes flows idle longer than the timeout; closing the socket
// ends the flow's reader, which removes it.
func (f *udpForwarder) sweep(now time.Time) {
	cutoff := now.Add(-protocol.UDPFlowIdleTimeout).UnixNano()

	f.mu.Lock()
	defer f.mu.Unlock()

	for _, flow := range f.flows {
		if flow.lastSeen.Load() < cutoff {
			flow.conn.Close()
		}
	}
}

func (f *udpForwarder) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, flow := range f.flows {
		flow.conn.Close()
	}
}
