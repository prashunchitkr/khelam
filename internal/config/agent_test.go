package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/prashunchitkr/khelam/internal/protocol"
)

// writeConfig writes an agent config file and a token file next to it,
// and returns the config path.
func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	t.Setenv(TokenEnv, "")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const fullConfig = `
server: tunnel.example.com:31000
name: lan-box
id_file: state/agent-id
token_file: token
fingerprint: sha256:abc
proxy_protocol: true
tunnels:
  - name: web
    local: localhost:8080
  - name: game
    protocol: udp
    local: localhost:19132
  - local: localhost:25565
`

func TestAgentConfigFile(t *testing.T) {
	path := writeConfig(t, fullConfig)

	cfg, err := GetAgentConfig([]string{"-config", path})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ServerAddress != "tunnel.example.com:31000" || cfg.Name != "lan-box" ||
		cfg.Fingerprint != "sha256:abc" || !cfg.ProxyProtocol {
		t.Fatalf("scalar fields not loaded: %+v", cfg)
	}

	if cfg.Token != "file-token" {
		t.Fatalf("Token = %q, want it read from the relative token_file", cfg.Token)
	}

	// The relative id_file is created next to the config file.
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "state", "agent-id")); err != nil {
		t.Fatalf("agent ID not created relative to config file: %v", err)
	}

	want := []TunnelConfig{
		{Name: "web", Protocol: protocol.ProtocolTCP, LocalAddress: "localhost:8080"},
		{Name: "game", Protocol: protocol.ProtocolUDP, LocalAddress: "localhost:19132"},
		{Name: "tcp-25565", Protocol: protocol.ProtocolTCP, LocalAddress: "localhost:25565"},
	}
	if len(cfg.Tunnels) != len(want) {
		t.Fatalf("Tunnels = %+v, want %+v", cfg.Tunnels, want)
	}
	for i := range want {
		if cfg.Tunnels[i] != want[i] {
			t.Errorf("tunnel %d = %+v, want %+v", i, cfg.Tunnels[i], want[i])
		}
	}
}

func TestAgentFlagsOverrideConfigFile(t *testing.T) {
	path := writeConfig(t, fullConfig)

	cfg, err := GetAgentConfig([]string{
		"-config", path,
		"-server", "other:1",
		"-token", "flag-token",
		"-tunnel", "only=localhost:1",
	})
	if err != nil {
		t.Fatal(err)
	}

	if cfg.ServerAddress != "other:1" {
		t.Errorf("ServerAddress = %q, want flag value", cfg.ServerAddress)
	}
	// -token replaces the file's token_file instead of conflicting.
	if cfg.Token != "flag-token" {
		t.Errorf("Token = %q, want flag value", cfg.Token)
	}
	if len(cfg.Tunnels) != 1 || cfg.Tunnels[0].Name != "only" {
		t.Errorf("Tunnels = %+v, want only the -tunnel flag", cfg.Tunnels)
	}
	// Values not overridden still come from the file.
	if cfg.Name != "lan-box" {
		t.Errorf("Name = %q, want file value", cfg.Name)
	}
}

func TestAgentConfigFileErrors(t *testing.T) {
	tests := map[string]string{
		"unknown key":        "server: a:1\nservre: typo\ntunnels: [{local: localhost:1}]\n",
		"token and file":     "server: a:1\ntoken: x\ntoken_file: token\ntunnels: [{local: localhost:1}]\n",
		"bad protocol":       "server: a:1\ntunnels: [{protocol: sctp, local: localhost:1}]\n",
		"missing local":      "server: a:1\ntunnels: [{name: x}]\n",
		"duplicate name":     "server: a:1\ntunnels: [{name: x, local: localhost:1}, {name: x, local: localhost:2}]\n",
		"no tunnels":         "server: a:1\n",
		"no server":          "tunnels: [{local: localhost:1}]\n",
		"ca and fingerprint": "server: a:1\ntls_ca: ca.pem\nfingerprint: sha256:x\ntunnels: [{local: localhost:1}]\n",
	}

	for name, yaml := range tests {
		path := writeConfig(t, yaml)
		if _, err := GetAgentConfig([]string{"-config", path, "-id-file", filepath.Join(t.TempDir(), "id")}); err == nil {
			t.Errorf("%s: GetAgentConfig succeeded, want error", name)
		}
	}
}

// TestExampleAgentConfig keeps examples/agent.yaml loadable.
func TestExampleAgentConfig(t *testing.T) {
	example, err := os.ReadFile("../../examples/agent.yaml")
	if err != nil {
		t.Fatal(err)
	}

	path := writeConfig(t, string(example))
	os.WriteFile(filepath.Join(filepath.Dir(path), "agent.token"), []byte("khelam_example"), 0o600)

	cfg, err := GetAgentConfig([]string{"-config", path, "-id-file", filepath.Join(t.TempDir(), "id")})
	if err != nil {
		t.Fatalf("examples/agent.yaml: %v", err)
	}
	if len(cfg.Tunnels) == 0 || cfg.Token != "khelam_example" {
		t.Fatalf("examples/agent.yaml loaded as %+v", cfg)
	}
}
