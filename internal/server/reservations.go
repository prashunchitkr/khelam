package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
)

// reservationKey identifies the owner of a sticky port: one named
// tunnel of one agent.
type reservationKey struct {
	AgentID  string            `json:"agent_id"`
	Tunnel   string            `json:"tunnel"`
	Protocol protocol.Protocol `json:"protocol"`
}

type reservation struct {
	reservationKey
	Port     uint32    `json:"port"`
	LastSeen time.Time `json:"last_seen"`
}

// Reservations remembers which public port each tunnel last had, so an
// agent gets the same address back when it reconnects. Each port has at
// most one owner. It is safe for concurrent use.
type Reservations struct {
	mu     sync.Mutex
	path   string
	byKey  map[reservationKey]*reservation
	byPort map[protocol.Protocol]map[uint32]reservationKey
}

type reservationFile struct {
	Reservations []*reservation `json:"reservations"`
}

// LoadReservations reads reservations from path. An empty path keeps
// reservations in memory only.
func LoadReservations(path string) (*Reservations, error) {
	r := &Reservations{
		path:   path,
		byKey:  make(map[reservationKey]*reservation),
		byPort: make(map[protocol.Protocol]map[uint32]reservationKey),
	}

	if path == "" {
		return r, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read reservations: %w", err)
	}

	var file reservationFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("cannot parse reservations %s: %w", path, err)
	}

	for _, res := range file.Reservations {
		r.set(res)
	}

	return r, nil
}

// Lookup returns the port reserved for key.
func (r *Reservations) Lookup(key reservationKey) (uint32, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	res, ok := r.byKey[key]
	if !ok {
		return 0, false
	}
	return res.Port, true
}

// ReservedByOther reports whether port is reserved by someone other
// than key.
func (r *Reservations) ReservedByOther(key reservationKey, port uint32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	owner, ok := r.byPort[key.Protocol][port]
	return ok && owner != key
}

// Reserve records port as key's sticky port, replacing key's previous
// reservation and any other owner of port, and persists the change.
func (r *Reservations) Reserve(key reservationKey, port uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.byKey[key]; ok {
		delete(r.byPort[key.Protocol], old.Port)
	}
	if owner, ok := r.byPort[key.Protocol][port]; ok {
		delete(r.byKey, owner)
	}

	r.set(&reservation{reservationKey: key, Port: port, LastSeen: time.Now()})
	return r.save()
}

// Touch records that key's reservation was just in use and persists it.
func (r *Reservations) Touch(key reservationKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	res, ok := r.byKey[key]
	if !ok {
		return nil
	}
	res.LastSeen = time.Now()
	return r.save()
}

func (r *Reservations) set(res *reservation) {
	r.byKey[res.reservationKey] = res
	if r.byPort[res.Protocol] == nil {
		r.byPort[res.Protocol] = make(map[uint32]reservationKey)
	}
	r.byPort[res.Protocol][res.Port] = res.reservationKey
}

// save writes all reservations atomically. The caller holds r.mu.
func (r *Reservations) save() error {
	if r.path == "" {
		return nil
	}

	file := reservationFile{Reservations: make([]*reservation, 0, len(r.byKey))}
	for _, res := range r.byKey {
		file.Reservations = append(file.Reservations, res)
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(r.path), ".reservations-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), r.path)
}
