package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/prashunchitkr/khelam/internal/protocol"
	"go.yaml.in/yaml/v3"
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
	// Flags: -token, -token-file (default: $KHELAM_TOKEN)
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

	// TUI selects the interactive terminal interface: "auto" (when
	// running in a terminal), "on" or "off".
	//
	// Flag: -tui
	TUI string
}

// agentFile is the YAML config file format for the agent. Field names
// mirror the flags.
type agentFile struct {
	Server        string       `yaml:"server"`
	Name          string       `yaml:"name"`
	IDFile        string       `yaml:"id_file"`
	Token         string       `yaml:"token"`
	TokenFile     string       `yaml:"token_file"`
	TLSCA         string       `yaml:"tls_ca"`
	Fingerprint   string       `yaml:"fingerprint"`
	ProxyProtocol bool         `yaml:"proxy_protocol"`
	Tunnels       []fileTunnel `yaml:"tunnels"`
}

type fileTunnel struct {
	Name     string `yaml:"name"`
	Protocol string `yaml:"protocol"`
	Local    string `yaml:"local"`
}

// GetAgentConfig parses CLI args for the agent. Values come from, in
// increasing priority: built-in defaults, the -config file, and flags
// given explicitly on the command line.
func GetAgentConfig(args []string) (AgentConfig, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	hostname, _ := os.Hostname()
	defaults := agentFile{
		Name:   hostname,
		IDFile: filepath.Join(defaultStateDir("agent"), "agent-id"),
	}

	var tunnels tunnelFlags
	configFile := fs.String("config", "", "YAML config file; flags given on the command line override it")
	serverAddress := fs.String("server", "", "address of the proxy server (e.g. 192.168.1.2:31000)")
	fs.Var(&tunnels, "tunnel", "local service to expose as [NAME=][PROTO://]HOST:PORT; repeatable, replaces the config file's tunnels (e.g. bedrock=udp://localhost:19132)")
	name := fs.String("name", defaults.Name, "name of this agent")
	idFile := fs.String("id-file", defaults.IDFile, "file holding this agent's ID (created if missing)")
	token := fs.String("token", "", "token to present to the server (default $"+TokenEnv+")")
	tokenFile := fs.String("token-file", "", "file containing the token to present to the server")
	tlsCA := fs.String("tls-ca", "", "CA certificate file to verify the server")
	fingerprint := fs.String("fingerprint", "", "pin the server certificate by fingerprint (sha256:<hex>)")
	proxyProtocol := fs.Bool("proxy-protocol", false, "send a PROXY protocol v2 header to local TCP services")
	tui := fs.String("tui", "auto", "interactive terminal interface: auto (when in a terminal), on, or off")

	if err := fs.Parse(args); err != nil {
		return AgentConfig{}, err
	}

	opts := defaults
	if *configFile != "" {
		if err := loadAgentFile(*configFile, &opts); err != nil {
			return AgentConfig{}, err
		}
	}

	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	override := func(flagName string, dst *string, value string) {
		if set[flagName] {
			*dst = value
		}
	}
	override("server", &opts.Server, *serverAddress)
	override("name", &opts.Name, *name)
	override("id-file", &opts.IDFile, *idFile)
	override("tls-ca", &opts.TLSCA, *tlsCA)
	override("fingerprint", &opts.Fingerprint, *fingerprint)
	if set["proxy-protocol"] {
		opts.ProxyProtocol = *proxyProtocol
	}
	// A token given on the command line replaces the file's token in
	// either form.
	if set["token"] || set["token-file"] {
		opts.Token, opts.TokenFile = *token, *tokenFile
	}

	tunnelConfigs := []TunnelConfig(tunnels)
	if len(tunnelConfigs) == 0 {
		var err error
		if tunnelConfigs, err = opts.tunnelConfigs(); err != nil {
			return AgentConfig{}, err
		}
	}

	switch *tui {
	case "auto", "on", "off":
	default:
		return AgentConfig{}, fmt.Errorf("-tui must be auto, on or off, not %q", *tui)
	}

	if opts.Server == "" {
		return AgentConfig{}, errors.New("a server address is required (-server or \"server\" in the config file)")
	}

	if len(tunnelConfigs) == 0 {
		return AgentConfig{}, errors.New("at least one tunnel is required (-tunnel or \"tunnels\" in the config file)")
	}

	if opts.TLSCA != "" && opts.Fingerprint != "" {
		return AgentConfig{}, errors.New("tls-ca and fingerprint are mutually exclusive")
	}

	resolvedToken, err := resolveToken(opts.Token, opts.TokenFile)
	if err != nil {
		return AgentConfig{}, err
	}

	agentID, err := loadOrCreateAgentID(opts.IDFile)
	if err != nil {
		return AgentConfig{}, err
	}

	cfg := AgentConfig{
		ServerAddress: opts.Server,
		Tunnels:       tunnelConfigs,
		Name:          opts.Name,
		AgentID:       agentID,
		Token:         resolvedToken,
		TLSCAFile:     opts.TLSCA,
		Fingerprint:   opts.Fingerprint,
		ProxyProtocol: opts.ProxyProtocol,
		TUI:           *tui,
	}

	return cfg, nil
}

// loadAgentFile decodes the config file at path over opts. Unknown keys
// are an error, to catch typos. Relative paths in the file are resolved
// against the file's directory.
func loadAgentFile(path string, opts *agentFile) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read config file: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(opts); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid config file %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	for _, p := range []*string{&opts.IDFile, &opts.TokenFile, &opts.TLSCA} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(dir, *p)
		}
	}

	return nil
}

// tunnelConfigs validates the config file's tunnels, applying the same
// defaults as -tunnel.
func (f agentFile) tunnelConfigs() ([]TunnelConfig, error) {
	var tunnels tunnelFlags

	for i, t := range f.Tunnels {
		if t.Local == "" {
			return nil, fmt.Errorf("config file tunnel %d: \"local\" is required", i+1)
		}

		spec := t.Local
		if t.Protocol != "" {
			spec = t.Protocol + "://" + spec
		}
		if t.Name != "" {
			spec = t.Name + "=" + spec
		}

		if err := tunnels.Set(spec); err != nil {
			return nil, fmt.Errorf("config file tunnel %d: %w", i+1, err)
		}
	}

	return tunnels, nil
}

// resolveToken returns the token given directly, read from tokenFile,
// or from the environment, in that order.
func resolveToken(token, tokenFile string) (string, error) {
	if token != "" && tokenFile != "" {
		return "", errors.New("token and token-file are mutually exclusive")
	}

	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("cannot read token file: %w", err)
		}
		return strings.TrimSpace(string(data)), nil
	}

	// Read the env fallback last so the secret is never shown as a flag
	// default in -h output.
	if token == "" {
		token = os.Getenv(TokenEnv)
	}
	return token, nil
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
