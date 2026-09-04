package config

import (
	"errors"
	"flag"
	"os"
)

// ServerConfig contains configuration required to start the server instance.
// These settings are populated from CLI flags
type ServerConfig struct {
	// ControlAddress is the network address where the server listens to the agents
	//
	// Flag: -listen
	//
	// Example:
	//  -listen 0.0.0.0:31000
	ControlAddress string
}

// GetServerConfig parses CLI args for the server
func GetServerConfig(args []string) (ServerConfig, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	controlAddress := fs.String("listen", "", "address of the control port (e.g. 0.0.0.0:31000)")

	if err := fs.Parse(args); err != nil {
		return ServerConfig{}, err
	}

	if fs.NFlag() == 0 {
		return ServerConfig{}, errors.New("no flags provided")
	}

	cfg := ServerConfig{
		ControlAddress: *controlAddress,
	}

	return cfg, nil
}
