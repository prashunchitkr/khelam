package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// TokenEnv is the environment variable used as the default auth token,
// so the secret does not have to appear in the process list.
const TokenEnv = "KHELAM_TOKEN"

// ServerConfig contains configuration required to start the server instance.
// These settings are populated from CLI flags
type ServerConfig struct {
	// ControlAddress is the UDP address where the server accepts QUIC
	// connections from agents.
	//
	// Flag: -listen
	//
	// Example:
	//  -listen 0.0.0.0:31000
	ControlAddress string

	// PortRangeStart is the first public port that can be allocated to agents.
	// TCP and UDP tunnels are allocated from separate pools over the same range.
	//
	// Flag: -port-range
	//
	// Example:
	//  -port-range 41000-41999
	PortRangeStart uint32

	// PortRangeEnd is the last public port that can be allocated to agents (inclusive).
	//
	// Flag: -port-range
	//
	// Example:
	//  -port-range 41000-41999
	PortRangeEnd uint32

	// Token is the shared secret agents must present to register. Empty
	// disables authentication.
	//
	// Flag: -token (default: $KHELAM_TOKEN)
	Token string

	// TLSCertFile and TLSKeyFile set the server certificate. Both or
	// neither must be set. When neither is set, a self-signed certificate
	// is generated and kept in StateDir.
	//
	// Flags: -tls-cert, -tls-key
	//
	// Example:
	//  -tls-cert server.crt -tls-key server.key
	TLSCertFile string
	TLSKeyFile  string

	// StateDir holds the sticky port reservations and the self-signed
	// certificate. Empty keeps both in memory only, so they are lost on
	// restart.
	//
	// Flag: -state-dir
	StateDir string

	// MaxConnsPerTunnel caps concurrent public connections (TCP) or
	// client flows (UDP) per tunnel. Zero means unlimited.
	//
	// Flag: -max-conns-per-tunnel
	MaxConnsPerTunnel int
}

// GetServerConfig parses CLI args for the server
func GetServerConfig(args []string) (ServerConfig, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	controlAddress := fs.String("listen", "0.0.0.0:31000", "UDP address for agent connections")
	portRange := fs.String("port-range", "41000-41999", "inclusive range of public ports to allocate")
	token := fs.String("token", "", "shared secret agents must present (default $"+TokenEnv+")")
	tlsCert := fs.String("tls-cert", "", "TLS certificate file (default: self-signed, kept in -state-dir)")
	tlsKey := fs.String("tls-key", "", "TLS private key file")
	stateDir := fs.String("state-dir", defaultStateDir("server"), "directory for port reservations and the self-signed certificate")
	maxConns := fs.Int("max-conns-per-tunnel", 1024, "maximum concurrent connections or UDP flows per tunnel (0 = unlimited)")

	if err := fs.Parse(args); err != nil {
		return ServerConfig{}, err
	}

	// Read the env fallback after parsing so the secret is never shown
	// as a flag default in -h output.
	if *token == "" {
		*token = os.Getenv(TokenEnv)
	}

	start, end, err := parsePortRange(*portRange)
	if err != nil {
		return ServerConfig{}, err
	}

	if (*tlsCert == "") != (*tlsKey == "") {
		return ServerConfig{}, errors.New("-tls-cert and -tls-key must be set together")
	}

	if *maxConns < 0 {
		return ServerConfig{}, errors.New("-max-conns-per-tunnel must not be negative")
	}

	cfg := ServerConfig{
		ControlAddress:    *controlAddress,
		PortRangeStart:    start,
		PortRangeEnd:      end,
		Token:             *token,
		TLSCertFile:       *tlsCert,
		TLSKeyFile:        *tlsKey,
		StateDir:          *stateDir,
		MaxConnsPerTunnel: *maxConns,
	}

	return cfg, nil
}

// defaultStateDir returns the per-user directory for khelam state, or
// empty if it cannot be determined.
func defaultStateDir(role string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "khelam", role)
}

func parsePortRange(rng string) (start, end uint32, err error) {
	_, err = fmt.Sscanf(rng, "%d-%d", &start, &end)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", rng, err)
	}

	if start < 1 || end > 65535 || start > end {
		return 0, 0, fmt.Errorf("invalid port range %q: expected START-END with 1 <= START <= END <= 65535", rng)
	}

	return start, end, nil
}
