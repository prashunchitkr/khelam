package config

import (
	"errors"
	"flag"
	"fmt"
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

	// PortRangeStart is the first public port that can be allocated to agents.
	//
	// Flag: -port-range
	//
	// Example:
	//  -port-range 41000-41999
	PortRangeStart uint32

	// PortRangeEnd is one past the last public port that can be allocated to agents.
	//
	// Flag: -port-range
	//
	// Example:
	//  -port-range 41000-41999
	PortRangeEnd uint32
}

// GetServerConfig parses CLI args for the server
func GetServerConfig(args []string) (ServerConfig, error) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	controlAddress := fs.String("listen", "", "address of the control port (e.g. 0.0.0.0:31000)")
	portRange := fs.String("port-range", "41000-41999", "range of public ports to allocate (e.g. 41000-41999)")

	if err := fs.Parse(args); err != nil {
		return ServerConfig{}, err
	}

	if fs.NFlag() == 0 {
		return ServerConfig{}, errors.New("no flags provided")
	}

	start, end, err := parsePortRange(*portRange)
	if err != nil {
		return ServerConfig{}, err
	}

	cfg := ServerConfig{
		ControlAddress: *controlAddress,
		PortRangeStart: start,
		PortRangeEnd:   end,
	}

	return cfg, nil
}

func parsePortRange(rng string) (start, end uint32, err error) {
	_, err = fmt.Sscanf(rng, "%d-%d", &start, &end)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid port range %q: %w", rng, err)
	}

	if start < 1 || end > 65535 || start >= end {
		return 0, 0, fmt.Errorf("invalid port range %q: expected START-END with 1 <= START < END <= 65535", rng)
	}

	return start, end, nil
}
