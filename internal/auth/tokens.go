// Package auth manages per-agent tokens. The server keeps only a
// SHA-256 hash of each token, so the tokens file holds no secrets.
package auth

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"
)

// tokenPrefix makes khelam tokens recognizable, e.g. by secret scanners.
const tokenPrefix = "khelam_"

// Entry is one named token in the tokens file.
type Entry struct {
	// Name identifies the token's holder. Agents registering with the
	// token are scoped to it, so they cannot take over agents registered
	// under another token.
	Name string `yaml:"name"`

	// SHA256 is the hex SHA-256 hash of the token.
	SHA256 string `yaml:"sha256"`
}

type tokensFile struct {
	Tokens []Entry `yaml:"tokens"`
}

type entry struct {
	name string
	hash [sha256.Size]byte
}

// Store authenticates agents against a tokens file. It is safe for
// concurrent use and can be reloaded while in use.
type Store struct {
	path string

	mu      sync.RWMutex
	entries []entry
}

// Load reads the tokens file at path.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Reload re-reads the tokens file. On error the previous tokens stay in
// effect.
func (s *Store) Reload() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("cannot read tokens file: %w", err)
	}

	entries, err := parse(data)
	if err != nil {
		return fmt.Errorf("invalid tokens file %s: %w", s.path, err)
	}

	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	return nil
}

func parse(data []byte) ([]entry, error) {
	var file tokensFile
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}

	entries := make([]entry, 0, len(file.Tokens))
	names := make(map[string]bool, len(file.Tokens))

	for i, t := range file.Tokens {
		if err := validateName(t.Name); err != nil {
			return nil, fmt.Errorf("token %d: %w", i+1, err)
		}
		if names[t.Name] {
			return nil, fmt.Errorf("duplicate token name %q", t.Name)
		}
		names[t.Name] = true

		hash, err := hex.DecodeString(t.SHA256)
		if err != nil || len(hash) != sha256.Size {
			return nil, fmt.Errorf("token %q: sha256 must be %d hex characters", t.Name, 2*sha256.Size)
		}

		e := entry{name: t.Name}
		copy(e.hash[:], hash)
		entries = append(entries, e)
	}

	return entries, nil
}

func validateName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	// Agent IDs are scoped as "<name>/<id>", so a slash in the name would
	// let one token's agents collide with another's.
	if strings.Contains(name, "/") {
		return fmt.Errorf("name %q must not contain '/'", name)
	}
	return nil
}

// Authenticate returns the name of the entry matching token. Every entry
// is compared in constant time, so timing reveals nothing about which
// entry, if any, matched.
func (s *Store) Authenticate(token string) (name string, ok bool) {
	hash := sha256.Sum256([]byte(token))

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, e := range s.entries {
		if subtle.ConstantTimeCompare(hash[:], e.hash[:]) == 1 {
			name, ok = e.name, true
		}
	}
	return name, ok
}

// Has reports whether a token named name is currently valid.
func (s *Store) Has(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, e := range s.entries {
		if e.name == name {
			return true
		}
	}
	return false
}

// Generate creates a new random token and returns it with the tokens
// file entry that accepts it.
func Generate(name string) (token string, e Entry, err error) {
	if err := validateName(name); err != nil {
		return "", Entry{}, err
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", Entry{}, err
	}

	token = tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))

	return token, Entry{Name: name, SHA256: hex.EncodeToString(hash[:])}, nil
}
