package server

import (
	"errors"
	"slices"
	"sync"
)

// errNoPorts is returned when the pool has no port to hand out.
var errNoPorts = errors.New("no public ports available")

// PortPool tracks the set of public port numbers available for
// allocation to tunnels. Allocation and release are safe for concurrent
// use.
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

// Allocate returns the least recently released free port for which
// skip returns false, marking it as in use. A nil skip accepts any port.
func (pp *PortPool) Allocate(skip func(port uint32) bool) (uint32, error) {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	for i, port := range pp.free {
		if skip != nil && skip(port) {
			continue
		}
		pp.free = slices.Delete(pp.free, i, i+1)
		pp.inUse[port] = struct{}{}
		return port, nil
	}

	return 0, errNoPorts
}

// Take marks a specific port as in use. It returns false if the port is
// not free.
func (pp *PortPool) Take(port uint32) bool {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	i := slices.Index(pp.free, port)
	if i < 0 {
		return false
	}

	pp.free = slices.Delete(pp.free, i, i+1)
	pp.inUse[port] = struct{}{}
	return true
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
