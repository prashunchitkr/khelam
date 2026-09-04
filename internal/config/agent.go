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
}

// GetAgentConfig parses CLI args for the agent
func GetAgentConfig(args []string) (AgentConfig, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	serverAddress := fs.String("server", "", "address of the proxy server (e.g. 192.168.1.2:31000)")
	tunnelAddress := fs.String("tunnel", "", "address of service to tunnel (e.g. localhost:8000)")

	if err := fs.Parse(args); err != nil {
		return AgentConfig{}, err
	}

	if fs.NFlag() == 0 {
		return AgentConfig{}, errors.New("no flags provided")
	}

	cfg := AgentConfig{
		ServerAddress: *serverAddress,
		TunnelAddress: *tunnelAddress,
	}

	return cfg, nil
}
