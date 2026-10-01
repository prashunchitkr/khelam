package tui

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/prashunchitkr/khelam/internal/agent"
)

type fakeAgent struct {
	status  agent.Status
	retries int
}

func (f *fakeAgent) Status() agent.Status { return f.status }
func (f *fakeAgent) RetryNow()            { f.retries++ }

var testNow = time.Date(2026, 10, 1, 21, 40, 0, 0, time.UTC)

func connectedAgent() *fakeAgent {
	return &fakeAgent{status: agent.Status{
		State:         agent.StateConnected,
		Since:         testNow.Add(-(2*time.Hour + 13*time.Minute)),
		ServerAddress: "tunnel.example.com:31000",
		Tunnels: []agent.TunnelStatus{
			{Name: "minecraft", Protocol: "tcp", LocalAddress: "localhost:25565", PublicAddress: "tunnel.example.com:41000", Active: 3, BytesIn: 1_200_000, BytesOut: 48_000_000},
			{Name: "bedrock", Protocol: "udp", LocalAddress: "localhost:19132", PublicAddress: "tunnel.example.com:41001", Active: 5, BytesIn: 310_000, BytesOut: 2_100_000},
		},
	}}
}

func newTestModel(agt Agent) Model {
	m := New(agt, "v1.0.0")
	m.now = func() time.Time { return testNow }
	m.width, m.height = 120, 30
	return m
}

func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

func key(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// plain renders the model without styling, for matching text.
func plain(m Model) string {
	return ansi.Strip(m.render())
}

func TestViewShowsTunnelsAndState(t *testing.T) {
	out := plain(newTestModel(connectedAgent()))

	for _, want := range []string{
		"khelam agent", "v1.0.0", "tunnel.example.com:31000",
		"connected 2h13m",
		"minecraft", "tunnel.example.com:41000", "localhost:25565", "1.2 MB", "48.0 MB",
		"bedrock", "udp", "tunnel.example.com:41001",
		"q quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}

	if strings.Contains(out, "r retry now") {
		t.Error("retry key shown while connected")
	}
}

func TestViewFitsWidth(t *testing.T) {
	m := newTestModel(connectedAgent())
	m.width = 40

	for _, line := range strings.Split(m.render(), "\n") {
		if w := ansi.StringWidth(line); w > 40 {
			t.Fatalf("line is %d columns wide, want <= 40: %q", w, ansi.Strip(line))
		}
	}
}

func TestViewRetrying(t *testing.T) {
	agt := connectedAgent()
	agt.status.State = agent.StateRetrying
	agt.status.RetryAt = testNow.Add(4 * time.Second)
	agt.status.Err = errors.New("no public port available")

	out := plain(newTestModel(agt))
	if !strings.Contains(out, "retrying in 4s") || !strings.Contains(out, "\n no public port available") {
		t.Errorf("view does not explain the retry on its own line:\n%s", out)
	}
	if !strings.Contains(out, "r retry now") {
		t.Error("retry key not offered while retrying")
	}
}

func TestViewStoppedWithError(t *testing.T) {
	agt := connectedAgent()
	agt.status.State = agent.StateStopped
	agt.status.Err = errors.New("invalid token")

	out := plain(newTestModel(agt))
	if !strings.Contains(out, "● stopped") || !strings.Contains(out, "\n invalid token") {
		t.Errorf("view does not show why the agent stopped:\n%s", out)
	}
}

func TestTickRefreshesStatus(t *testing.T) {
	agt := connectedAgent()
	m := newTestModel(agt)

	agt.status.Tunnels[0].Name = "renamed"
	m, cmd := update(t, m, tickMsg(testNow))

	if !strings.Contains(plain(m), "renamed") {
		t.Error("tick did not refresh the status")
	}
	if cmd == nil {
		t.Error("tick did not schedule the next tick")
	}
}

func TestCopyAddress(t *testing.T) {
	m := newTestModel(connectedAgent())

	m, _ = update(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	m, cmd := update(t, m, key("c"))

	if cmd == nil {
		t.Fatal("copy returned no command")
	}
	if !strings.Contains(plain(m), "copied tunnel.example.com:41001") {
		t.Errorf("copy notice missing for the selected tunnel:\n%s", plain(m))
	}
}

func TestCopyWithoutAddress(t *testing.T) {
	agt := connectedAgent()
	agt.status.Tunnels[0].PublicAddress = ""

	m, cmd := update(t, newTestModel(agt), key("c"))
	if cmd != nil {
		t.Error("copy of a missing address returned a command")
	}
	if !strings.Contains(plain(m), "no public address yet") {
		t.Error("missing-address notice not shown")
	}
}

func TestRetryKeyOnlyWhileRetrying(t *testing.T) {
	agt := connectedAgent()
	m := newTestModel(agt)

	update(t, m, key("r"))
	if agt.retries != 0 {
		t.Fatal("RetryNow called while connected")
	}

	agt.status.State = agent.StateRetrying
	m, _ = update(t, m, tickMsg(testNow))
	update(t, m, key("r"))
	if agt.retries != 1 {
		t.Fatalf("RetryNow called %d times while retrying, want 1", agt.retries)
	}
}

func TestToggleLogs(t *testing.T) {
	m := newTestModel(connectedAgent())
	m, _ = update(t, m, logMsg("21:40:12 INF connection opened tunnel=minecraft"))

	if !strings.Contains(plain(m), "connection opened") {
		t.Fatal("log line not shown")
	}

	m, _ = update(t, m, key("l"))
	if strings.Contains(plain(m), "connection opened") {
		t.Fatal("log line still shown after hiding the log")
	}
}

func TestLogsKeepMostRecent(t *testing.T) {
	m := newTestModel(connectedAgent())
	for i := range maxLogLines + 10 {
		m, _ = update(t, m, logMsg(strings.Repeat("x", i%3+1)))
	}

	if len(m.logs) != maxLogLines {
		t.Fatalf("kept %d log lines, want %d", len(m.logs), maxLogLines)
	}
}

func TestQuit(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{key("q"), {Code: 'c', Mod: tea.ModCtrl}} {
		_, cmd := update(t, newTestModel(connectedAgent()), k)
		if cmd == nil {
			t.Fatalf("%s returned no command", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s did not quit", k)
		}
	}
}

func TestFormatRecord(t *testing.T) {
	r := slog.NewRecord(testNow, slog.LevelWarn, "tunnel lost", 0)
	r.AddAttrs(slog.String("error", "timeout reached"), slog.Int("retryIn", 2))

	got := formatRecord(r, []slog.Attr{slog.String("component", "agent")})
	want := `21:40:00 WRN tunnel lost component=agent error="timeout reached" retryIn=2`
	if got != want {
		t.Fatalf("formatRecord = %q, want %q", got, want)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := map[int64]string{0: "0 B", 999: "999 B", 1000: "1.0 kB", 1_234_567: "1.2 MB", 5_000_000_000: "5.0 GB"}
	for n, want := range tests {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		45 * time.Second:               "45s",
		3*time.Minute + 12*time.Second: "3m12s",
		2*time.Hour + 13*time.Minute:   "2h13m",
		50*time.Hour + 30*time.Minute:  "2d02h",
		1500 * time.Millisecond:        "2s",
	}
	for d, want := range tests {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
