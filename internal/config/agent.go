package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/prashunchitkr/khelamgg/internal/protocol"
)

// TunnelConfig describes one local service to expose.
type TunnelConfig struct {
	// Name is unique per agent and keys the tunnel's sticky public port.
	Name string

	Protocol protocol.Protocol

	// LocalAddress is the address of the local service.
	LocalAddress string
}

// AgentConfig contains the configuration required to start the agent instance.
// These settings are populated from CLI flags.
type AgentConfig struct {
	// ServerAddress is the UDP address of the Proxy Server.
	//
	// Flag: -server
	//
	// Example:
	//   -server 192.168.1.2:31000
	ServerAddress string

	// Tunnels are the local services to expose.
	//
	// Flag: -tunnel [NAME=][PROTO://]HOST:PORT (repeatable; PROTO is tcp
	// or udp, default tcp; NAME defaults to PROTO-PORT)
	//
	// Example:
	//   -tunnel localhost:8000 -tunnel bedrock=udp://localhost:19132
	Tunnels []TunnelConfig

	// Name identifies the agent to the server in logs. Defaults to the hostname.
	//
	// Flag: -name
	//
	// Example:
	//   -name minecraft
	Name string

	// AgentID is a stable random identifier for this agent install. The
	// server keys sticky ports on it.
	//
	// Flag: -id-file (file holding the ID; created if missing)
	AgentID string

	// Token is the shared secret presented to the server.
	//
	// Flag: -token (default: $KHELAM_TOKEN)
	Token string

	// TLSCAFile is a PEM file of CA certificates used to verify the
	// server instead of the system roots.
	//
	// Flag: -tls-ca
	TLSCAFile string

	// Fingerprint pins the server certificate by its SHA-256 fingerprint
	// ("sha256:<hex>"), as logged by the server at startup. Use this for
	// a server with a self-signed certificate.
	//
	// Flag: -fingerprint
	Fingerprint string

	// ProxyProtocol prepends a PROXY protocol v2 header to every TCP
	// connection made to a local service, carrying the real client
	// address.
	//
	// Flag: -proxy-protocol
	ProxyProtocol bool
}

// GetAgentConfig parses CLI args for the agent
func GetAgentConfig(args []string) (AgentConfig, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	hostname, _ := os.Hostname()

	var tunnels tunnelFlags
	serverAddress := fs.String("server", "", "address of the proxy server (e.g. 192.168.1.2:31000)")
	fs.Var(&tunnels, "tunnel", "local service to expose as [NAME=][PROTO://]HOST:PORT; repeatable (e.g. bedrock=udp://localhost:19132)")
	name := fs.String("name", hostname, "name of this agent")
	idFile := fs.String("id-file", filepath.Join(defaultStateDir("agent"), "agent-id"), "file holding this agent's ID (created if missing)")
	token := fs.String("token", "", "shared secret to present to the server (default $"+TokenEnv+")")
	tlsCA := fs.String("tls-ca", "", "CA certificate file to verify the server")
	fingerprint := fs.String("fingerprint", "", "pin the server certificate by fingerprint (sha256:<hex>)")
	proxyProtocol := fs.Bool("proxy-protocol", false, "send a PROXY protocol v2 header to local TCP services")

	if err := fs.Parse(args); err != nil {
		return AgentConfig{}, err
	}

	// Read the env fallback after parsing so the secret is never shown
	// as a flag default in -h output.
	if *token == "" {
		*token = os.Getenv(TokenEnv)
	}

	if *serverAddress == "" {
		return AgentConfig{}, errors.New("-server is required")
	}

	if len(tunnels) == 0 {
		return AgentConfig{}, errors.New("at least one -tunnel is required")
	}

	if *tlsCA != "" && *fingerprint != "" {
		return AgentConfig{}, errors.New("-tls-ca and -fingerprint are mutually exclusive")
	}

	agentID, err := loadOrCreateAgentID(*idFile)
	if err != nil {
		return AgentConfig{}, err
	}

	cfg := AgentConfig{
		ServerAddress: *serverAddress,
		Tunnels:       tunnels,
		Name:          *name,
		AgentID:       agentID,
		Token:         *token,
		TLSCAFile:     *tlsCA,
		Fingerprint:   *fingerprint,
		ProxyProtocol: *proxyProtocol,
	}

	return cfg, nil
}

// tunnelFlags collects repeated -tunnel flags.
type tunnelFlags []TunnelConfig

func (t *tunnelFlags) String() string {
	parts := make([]string, len(*t))
	for i, tc := range *t {
		parts[i] = fmt.Sprintf("%s=%s://%s", tc.Name, tc.Protocol, tc.LocalAddress)
	}
	return strings.Join(parts, " ")
}

func (t *tunnelFlags) Set(value string) error {
	tc, err := ParseTunnel(value)
	if err != nil {
		return err
	}

	for _, existing := range *t {
		if existing.Name == tc.Name {
			return fmt.Errorf("duplicate tunnel name %q; set distinct names with NAME=", tc.Name)
		}
	}

	*t = append(*t, tc)
	return nil
}

// ParseTunnel parses a tunnel spec of the form [NAME=][PROTO://]HOST:PORT.
func ParseTunnel(spec string) (TunnelConfig, error) {
	tc := TunnelConfig{Protocol: protocol.ProtocolTCP}
	rest := spec

	if name, after, ok := strings.Cut(rest, "="); ok {
		tc.Name, rest = name, after
		if tc.Name == "" {
			return TunnelConfig{}, fmt.Errorf("invalid tunnel %q: empty name", spec)
		}
	}

	if proto, after, ok := strings.Cut(rest, "://"); ok {
		tc.Protocol, rest = protocol.Protocol(proto), after
		if tc.Protocol != protocol.ProtocolTCP && tc.Protocol != protocol.ProtocolUDP {
			return TunnelConfig{}, fmt.Errorf("invalid tunnel %q: protocol must be tcp or udp", spec)
		}
	}

	_, port, err := net.SplitHostPort(rest)
	if err != nil {
		return TunnelConfig{}, fmt.Errorf("invalid tunnel %q: %w", spec, err)
	}
	tc.LocalAddress = rest

	if tc.Name == "" {
		tc.Name = fmt.Sprintf("%s-%s", tc.Protocol, port)
	}

	return tc, nil
}

// loadOrCreateAgentID reads the agent ID from path, generating and
// saving a random one on first use.
func loadOrCreateAgentID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot read agent ID: %w", err)
	}

	b := make([]byte, 16)
	rand.Read(b)
	id := hex.EncodeToString(b)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("cannot create agent ID directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("cannot save agent ID: %w", err)
	}

	return id, nil
}
