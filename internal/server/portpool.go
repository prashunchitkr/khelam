package server

import (
	"errors"
	"sync"
)

// PortPool tracks the set of public port numbers available for
// allocation to tunnel agents. Allocation and release are safe for
// concurrent use.
//
// Ports are handed out first-in, first-out so a just-released port is
// reused as late as possible, which keeps stale clients of a previous
// tunnel from landing on a new one.
type PortPool struct {
	mu    sync.Mutex
	free  []uint32
	inUse map[uint32]struct{}
}

// NewPortPool returns a pool holding every port in the inclusive
// range [start, end].
func NewPortPool(start, end uint32) *PortPool {
	free := make([]uint32, 0, int(end-start)+1)
	for port := start; port <= end; port++ {
		free = append(free, port)
	}
	return &PortPool{
		free:  free,
		inUse: make(map[uint32]struct{}),
	}
}

// Allocate returns the least recently released free port, marking it
// as in use.
func (pp *PortPool) Allocate() (uint32, error) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if len(pp.free) == 0 {
		return 0, errors.New("no public ports available")
	}

	port := pp.free[0]
	pp.free = pp.free[1:]
	pp.inUse[port] = struct{}{}
	return port, nil
}

// Release returns a port to the back of the pool so it can be reused.
// Releasing a port that is not in use is a no-op.
func (pp *PortPool) Release(port uint32) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if _, ok := pp.inUse[port]; !ok {
		return
	}

	delete(pp.inUse, port)
	pp.free = append(pp.free, port)
}

// Free returns the number of ports currently available.
func (pp *PortPool) Free() int {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	return len(pp.free)
}
