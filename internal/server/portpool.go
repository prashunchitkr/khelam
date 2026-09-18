package server

import (
	"errors"
	"sync"
)

// PortPool tracks the set of public port numbers available for
// allocation to tunnel agents. Allocation and release are safe for
// concurrent use.
type PortPool struct {
	mu   sync.Mutex
	free []uint32
}

// NewPortPool returns a pool holding every port in the half-open
// range [start, end).
func NewPortPool(start, end uint32) *PortPool {
	free := make([]uint32, 0, int(end-start))
	for port := start; port < end; port++ {
		free = append(free, port)
	}
	return &PortPool{free: free}
}

// Allocate returns the next free port, marking it as in use.
func (pp *PortPool) Allocate() (uint32, error) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if len(pp.free) == 0 {
		return 0, errors.New("no public ports available")
	}

	port := pp.free[len(pp.free)-1]
	pp.free = pp.free[:len(pp.free)-1]
	return port, nil
}

// Release returns a port to the pool so it can be reused.
func (pp *PortPool) Release(port uint32) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	pp.free = append(pp.free, port)
}
