package config

import (
	"errors"
	"flag"
	"os"
)

// AgentConfig contains the configuration required to start the agent instance.
// These settings are populated from CLI flags.
type AgentConfig struct {
	// ServerAddress is the network address of the Proxy Server.
	//
	// Flag: -server
	//
	// Example:
	//   -server 192.168.1.2:31000
	ServerAddress string

	// TunnelAddress is the network address of the local service to tunnel.
	//
	// Flag: -tunnel
	//
	// Example:
	//   -tunnel localhost:8000
	TunnelAddress string

	// Name identifies the agent to the server. Defaults to the hostname.
	//
	// Flag: -name
	//
	// Example:
	//   -name minecraft
	Name string

	// Token is the shared secret presented to the server.
	//
	// Flag: -token (default: $KHELAM_TOKEN)
	Token string

	// TLS enables TLS on the connection to the server.
	//
	// Flag: -tls
	TLS bool

	// TLSCAFile is a PEM file of CA certificates used to verify the
	// server instead of the system roots, e.g. for a self-signed server.
	// Setting it implies TLS.
	//
	// Flag: -tls-ca
	TLSCAFile string

	// ProxyProtocol prepends a PROXY protocol v2 header to every
	// connection made to the local service, carrying the real client
	// address.
	//
	// Flag: -proxy-protocol
	ProxyProtocol bool
}

// GetAgentConfig parses CLI args for the agent
func GetAgentConfig(args []string) (AgentConfig, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	hostname, _ := os.Hostname()

	serverAddress := fs.String("server", "", "address of the proxy server (e.g. 192.168.1.2:31000)")
	tunnelAddress := fs.String("tunnel", "", "address of service to tunnel (e.g. localhost:8000)")
	name := fs.String("name", hostname, "name of this agent")
	token := fs.String("token", "", "shared secret to present to the server (default $"+TokenEnv+")")
	useTLS := fs.Bool("tls", false, "connect to the server over TLS")
	tlsCA := fs.String("tls-ca", "", "CA certificate file to verify the server (implies -tls)")
	proxyProtocol := fs.Bool("proxy-protocol", false, "send a PROXY protocol v2 header to the local service")

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

	if *tunnelAddress == "" {
		return AgentConfig{}, errors.New("-tunnel is required")
	}

	cfg := AgentConfig{
		ServerAddress: *serverAddress,
		TunnelAddress: *tunnelAddress,
		Name:          *name,
		Token:         *token,
		TLS:           *useTLS || *tlsCA != "",
		TLSCAFile:     *tlsCA,
		ProxyProtocol: *proxyProtocol,
	}

	return cfg, nil
}
