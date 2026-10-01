// Package tui is the agent's interactive terminal interface: connection
// state, each tunnel's public address and traffic, and recent log lines.
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/prashunchitkr/khelam/internal/agent"
)

const (
	refreshInterval = 500 * time.Millisecond
	maxLogLines     = 500
	noticeDuration  = 3 * time.Second
)

// Agent is the part of *agent.Agent the TUI uses.
type Agent interface {
	Status() agent.Status
	RetryNow()
}

type tickMsg time.Time

// logMsg is one formatted log line from the agent.
type logMsg string

// Model is the Bubble Tea model for the agent TUI.
type Model struct {
	agent   Agent
	version string

	status   agent.Status
	logs     []string
	selected int
	hideLogs bool

	notice      string
	noticeUntil time.Time

	width, height int
	now           func() time.Time
}

// New returns a model showing agt.
func New(agt Agent, version string) Model {
	return Model{
		agent:   agt,
		version: version,
		status:  agt.Status(),
		now:     time.Now,
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tick()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.status = m.agent.Status()
		return m, tick()

	case logMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > maxLogLines {
			m.logs = m.logs[len(m.logs)-maxLogLines:]
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}

	case "down", "j":
		if m.selected < len(m.status.Tunnels)-1 {
			m.selected++
		}

	case "c":
		if m.selected >= len(m.status.Tunnels) {
			return m, nil
		}
		address := m.status.Tunnels[m.selected].PublicAddress
		if address == "" {
			return m.withNotice("no public address yet"), nil
		}
		return m.withNotice("copied " + address), tea.SetClipboard(address)

	case "r":
		if m.status.State == agent.StateRetrying {
			m.agent.RetryNow()
			return m.withNotice("retrying now"), nil
		}

	case "l":
		m.hideLogs = !m.hideLogs
	}

	return m, nil
}

func (m Model) withNotice(notice string) Model {
	m.notice = notice
	m.noticeUntil = m.now().Add(noticeDuration)
	return m
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true)
	faintStyle    = lipgloss.NewStyle().Faint(true)
	headerStyle   = lipgloss.NewStyle().Bold(true).Faint(true)
	selectedStyle = lipgloss.NewStyle().Reverse(true)
	stateStyles   = map[agent.State]lipgloss.Style{
		agent.StateConnecting: lipgloss.NewStyle().Foreground(lipgloss.Color("4")),
		agent.StateConnected:  lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		agent.StateRetrying:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		agent.StateStopped:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
)

func (m Model) View() tea.View {
	view := tea.NewView(m.render())
	view.AltScreen = true
	return view
}

func (m Model) render() string {
	width := m.width
	if width <= 0 {
		width = 100
	}
	rule := faintStyle.Render(strings.Repeat("─", width))

	var lines []string
	lines = append(lines, m.renderHeader()...)
	lines = append(lines, rule)
	lines = append(lines, m.renderTunnels()...)

	footer := []string{rule, m.renderHelp()}

	if !m.hideLogs {
		lines = append(lines, rule)
		lines = append(lines, m.renderLogs(m.height-len(lines)-len(footer))...)
	}
	lines = append(lines, footer...)

	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	return strings.Join(lines, "\n")
}

// renderHeader returns the title line and, while retrying or stopped
// with an error, a line explaining it. Errors are often long, so they
// get a line of their own.
func (m Model) renderHeader() []string {
	s := m.status
	style := stateStyles[s.State]

	lines := []string{fmt.Sprintf(" %s %s · %s · %s",
		titleStyle.Render("khelam agent"), faintStyle.Render(m.version), s.ServerAddress,
		style.Render("● "+m.describeState()))}

	if s.Err != nil && (s.State == agent.StateRetrying || s.State == agent.StateStopped) {
		lines = append(lines, style.Render(" "+s.Err.Error()))
	}
	return lines
}

// describeState names the connection state and how long it has lasted
// or will last.
func (m Model) describeState() string {
	s := m.status
	now := m.now()

	switch s.State {
	case agent.StateConnected:
		return "connected " + formatDuration(now.Sub(s.Since))
	case agent.StateRetrying:
		wait := max(s.RetryAt.Sub(now), 0)
		return "retrying in " + formatDuration(wait)
	case agent.StateStopped:
		return "stopped"
	default:
		return "connecting…"
	}
}

func (m Model) renderTunnels() []string {
	rows := [][]string{{"TUNNEL", "PROTO", "PUBLIC ADDRESS", "LOCAL", "CONNS", "IN", "OUT"}}
	for _, t := range m.status.Tunnels {
		address := t.PublicAddress
		if address == "" {
			address = "-"
		}
		rows = append(rows, []string{
			t.Name, string(t.Protocol), address, t.LocalAddress,
			fmt.Sprint(t.Active), formatBytes(t.BytesIn), formatBytes(t.BytesOut),
		})
	}

	// Numeric columns are right-aligned.
	rightAligned := map[int]bool{4: true, 5: true, 6: true}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}

	lines := make([]string, len(rows))
	for r, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			pad := strings.Repeat(" ", widths[i]-lipgloss.Width(cell))
			if rightAligned[i] {
				cells[i] = pad + cell
			} else {
				cells[i] = cell + pad
			}
		}
		line := "  " + strings.Join(cells, "   ")

		switch {
		case r == 0:
			line = headerStyle.Render(line)
		case r-1 == m.selected:
			line = selectedStyle.Render("▸" + line[1:])
		case m.status.State != agent.StateConnected:
			// Addresses are stale while disconnected.
			line = faintStyle.Render(line)
		}
		lines[r] = line
	}

	return lines
}

// renderLogs returns the most recent log lines that fit in height.
func (m Model) renderLogs(height int) []string {
	height = max(height, 1)
	if m.height <= 0 {
		height = 10
	}

	start := max(len(m.logs)-height, 0)
	lines := make([]string, 0, height)
	for _, line := range m.logs[start:] {
		lines = append(lines, " "+line)
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

func (m Model) renderHelp() string {
	keys := []string{"↑/↓ select", "c copy address"}
	if m.status.State == agent.StateRetrying {
		keys = append(keys, "r retry now")
	}
	if m.hideLogs {
		keys = append(keys, "l show log")
	} else {
		keys = append(keys, "l hide log")
	}
	keys = append(keys, "q quit")

	help := faintStyle.Render(" " + strings.Join(keys, " · "))
	if m.notice != "" && m.now().Before(m.noticeUntil) {
		help += "   " + titleStyle.Render(m.notice)
	}
	return help
}
