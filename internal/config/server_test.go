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
