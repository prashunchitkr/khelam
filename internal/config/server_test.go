package config

import "testing"

func TestParsePortRange(t *testing.T) {
	tests := []struct {
		in         string
		start, end uint32
		wantErr    bool
	}{
		{in: "41000-41999", start: 41000, end: 41999},
		{in: "5000-5000", start: 5000, end: 5000},
		{in: "1-65535", start: 1, end: 65535},
		{in: "0-100", wantErr: true},
		{in: "100-65536", wantErr: true},
		{in: "200-100", wantErr: true},
		{in: "abc", wantErr: true},
	}

	for _, tt := range tests {
		start, end, err := parsePortRange(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parsePortRange(%q) succeeded, want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePortRange(%q): %v", tt.in, err)
			continue
		}
		if start != tt.start || end != tt.end {
			t.Errorf("parsePortRange(%q) = %d-%d, want %d-%d", tt.in, start, end, tt.start, tt.end)
		}
	}
}

func TestServerConfigDefaults(t *testing.T) {
	cfg, err := GetServerConfig(nil)
	if err != nil {
		t.Fatalf("GetServerConfig: %v", err)
	}
	if cfg.ControlAddress == "" {
		t.Fatal("ControlAddress has no default")
	}
}

func TestParseTunnel(t *testing.T) {
	tests := []struct {
		in      string
		want    TunnelConfig
		wantErr bool
	}{
		{in: "localhost:8000", want: TunnelConfig{Name: "tcp-8000", Protocol: "tcp", LocalAddress: "localhost:8000"}},
		{in: "udp://localhost:19132", want: TunnelConfig{Name: "udp-19132", Protocol: "udp", LocalAddress: "localhost:19132"}},
		{in: "mc=tcp://127.0.0.1:25565", want: TunnelConfig{Name: "mc", Protocol: "tcp", LocalAddress: "127.0.0.1:25565"}},
		{in: "web=[::1]:80", want: TunnelConfig{Name: "web", Protocol: "tcp", LocalAddress: "[::1]:80"}},
		{in: "sctp://localhost:1", wantErr: true},
		{in: "=localhost:1", wantErr: true},
		{in: "localhost", wantErr: true},
	}

	for _, tt := range tests {
		got, err := ParseTunnel(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseTunnel(%q) succeeded, want error", tt.in)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseTunnel(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
}

func TestAgentIDIsCreatedOnceAndReused(t *testing.T) {
	path := t.TempDir() + "/nested/agent-id"

	first, err := loadOrCreateAgentID(path)
	if err != nil || first == "" {
		t.Fatalf("first load: %q, %v", first, err)
	}

	second, err := loadOrCreateAgentID(path)
	if err != nil || second != first {
		t.Fatalf("second load = %q, %v; want %q", second, err, first)
	}
}
