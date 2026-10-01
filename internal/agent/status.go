package agent

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prashunchitkr/khelam/internal/config"
	"github.com/prashunchitkr/khelam/internal/protocol"
)

// State is the agent's connection state.
type State int

const (
	// StateConnecting: dialing the server or registering tunnels.
	StateConnecting State = iota
	// StateConnected: tunnels are open and relaying traffic.
	StateConnected
	// StateRetrying: the last attempt failed; the next starts at RetryAt.
	StateRetrying
	// StateStopped: Run has returned, with Err if it failed.
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateRetrying:
		return "retrying"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Status is a snapshot of the agent for display.
type Status struct {
	State State
	// Since is when the agent entered State.
	Since time.Time
	// RetryAt is when the next attempt starts, in StateRetrying.
	RetryAt time.Time
	// Err is why the last attempt failed (StateRetrying) or why the agent
	// stopped (StateStopped).
	Err error

	ServerAddress string
	// Tunnels are in configuration order.
	Tunnels []TunnelStatus
}

// TunnelStatus describes one tunnel. Counters cover the agent's whole
// lifetime, across reconnects.
type TunnelStatus struct {
	Name         string
	Protocol     protocol.Protocol
	LocalAddress string

	// PublicAddress is where clients connect, or empty before the first
	// registration. It keeps its last value while disconnected.
	PublicAddress string

	// Active is the number of open TCP connections or UDP flows.
	Active int64
	// Total is the number of TCP connections or UDP flows so far.
	Total int64
	// BytesIn counts bytes from public clients, BytesOut bytes to them.
	BytesIn, BytesOut int64
}

// tunnelStats holds the live counters for one tunnel.
type tunnelStats struct {
	cfg        config.TunnelConfig
	publicPort atomic.Uint32

	active, total     atomic.Int64
	bytesIn, bytesOut atomic.Int64
}

// status is the agent's mutable state behind Status.
type status struct {
	mu      sync.Mutex
	state   State
	since   time.Time
	retryAt time.Time
	err     error

	serverHost string
	// tunnels are in configuration order; byName indexes them.
	tunnels []*tunnelStats
	byName  map[string]*tunnelStats

	// retryNow cuts a retry wait short.
	retryNow chan struct{}
}

func newStatus(cfg config.AgentConfig) *status {
	host, _, err := net.SplitHostPort(cfg.ServerAddress)
	if err != nil {
		host = cfg.ServerAddress
	}

	s := &status{
		state:      StateConnecting,
		since:      time.Now(),
		serverHost: host,
		byName:     make(map[string]*tunnelStats, len(cfg.Tunnels)),
		retryNow:   make(chan struct{}, 1),
	}

	for _, tc := range cfg.Tunnels {
		stats := &tunnelStats{cfg: tc}
		s.tunnels = append(s.tunnels, stats)
		s.byName[tc.Name] = stats
	}

	return s
}

func (s *status) set(state State, err error, retryAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.state, s.err, s.retryAt = state, err, retryAt
	s.since = time.Now()
}

// Status returns a snapshot of the agent's state and tunnel counters.
// It is safe to call from any goroutine.
func (a *Agent) Status() Status {
	s := a.status

	s.mu.Lock()
	snapshot := Status{
		State:         s.state,
		Since:         s.since,
		RetryAt:       s.retryAt,
		Err:           s.err,
		ServerAddress: a.cfg.ServerAddress,
	}
	s.mu.Unlock()

	for _, t := range s.tunnels {
		ts := TunnelStatus{
			Name:         t.cfg.Name,
			Protocol:     t.cfg.Protocol,
			LocalAddress: t.cfg.LocalAddress,
			Active:       t.active.Load(),
			Total:        t.total.Load(),
			BytesIn:      t.bytesIn.Load(),
			BytesOut:     t.bytesOut.Load(),
		}
		if port := t.publicPort.Load(); port != 0 {
			ts.PublicAddress = net.JoinHostPort(s.serverHost, strconv.FormatUint(uint64(port), 10))
		}
		snapshot.Tunnels = append(snapshot.Tunnels, ts)
	}

	return snapshot
}

// RetryNow ends the current wait between reconnect attempts, if any.
func (a *Agent) RetryNow() {
	select {
	case a.status.retryNow <- struct{}{}:
	default:
	}
}

// countedConn counts bytes read from and written to a tunnel stream.
type countedConn struct {
	net.Conn
	read, written *atomic.Int64
}

func (c *countedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, err
}

func (c *countedConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.written.Add(int64(n))
	return n, err
}

// CloseWrite keeps half-close working through the wrapper.
func (c *countedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return c.Conn.Close()
}
