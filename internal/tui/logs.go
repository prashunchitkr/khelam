package tui

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// logBuffer bounds lines waiting to reach the TUI. Lines beyond it are
// dropped rather than blocking the agent.
const logBuffer = 1024

// LogHandler is a slog.Handler that sends formatted records to the TUI's
// log pane. Records logged before the TUI starts are held until then.
type LogHandler struct {
	level slog.Leveler
	attrs []slog.Attr
	sink  *logSink
}

type logSink struct {
	lines chan string
	once  sync.Once
}

// NewLogHandler returns a handler for records at level and above.
func NewLogHandler(level slog.Leveler) *LogHandler {
	return &LogHandler{
		level: level,
		sink:  &logSink{lines: make(chan string, logBuffer)},
	}
}

// Attach starts delivering log lines to p.
func (h *LogHandler) Attach(p *tea.Program) {
	h.sink.once.Do(func() {
		go func() {
			for line := range h.sink.lines {
				p.Send(logMsg(line))
			}
		}()
	})
}

func (h *LogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *LogHandler) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.sink.lines <- formatRecord(r, h.attrs):
	default:
	}
	return nil
}

func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

// WithGroup is not needed by the agent; group names are dropped.
func (h *LogHandler) WithGroup(string) slog.Handler {
	return h
}

var levelNames = map[slog.Level]string{
	slog.LevelDebug: "DBG",
	slog.LevelInfo:  "INF",
	slog.LevelWarn:  "WRN",
	slog.LevelError: "ERR",
}

// formatRecord renders a record as one compact line:
// "21:40:12 INF connection opened tunnel=web clientAddr=1.2.3.4:5".
func formatRecord(r slog.Record, attrs []slog.Attr) string {
	var b strings.Builder

	level, ok := levelNames[r.Level]
	if !ok {
		level = r.Level.String()
	}
	fmt.Fprintf(&b, "%s %s %s", r.Time.Format("15:04:05"), level, r.Message)

	writeAttr := func(a slog.Attr) bool {
		value := a.Value.Resolve().String()
		if strings.ContainsAny(value, " \t\n\"") || value == "" {
			value = fmt.Sprintf("%q", value)
		}
		fmt.Fprintf(&b, " %s=%s", a.Key, value)
		return true
	}

	for _, a := range attrs {
		writeAttr(a)
	}
	r.Attrs(writeAttr)

	return b.String()
}
