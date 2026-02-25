package progress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"charm.land/lipgloss/v2"
)

// ErrUnknownFormat is returned when an unrecognized log format is requested.
var ErrUnknownFormat = errors.New("unknown log format")

// Compile-time interface check.
var _ slog.Handler = (*PrettyHandler)(nil)

// PrettyHandler writes colored output to an io.Writer. Attributes registered
// via WithAttrs and WithGroup are accumulated in the prefix field as
// "key=val " and "group." fragments respectively; Handle prepends prefix to
// every message so these context attributes are always visible. Inline
// record attributes passed to individual log calls are suppressed in pretty
// mode (they are available in json/text modes via stdlib handlers).
type PrettyHandler struct {
	out    io.Writer
	level  slog.Leveler
	mu     *sync.Mutex
	prefix string // accumulated "[group] key=val " prefix from WithAttrs/WithGroup
}

// NewPrettyHandler returns a PrettyHandler that writes to out at the given level.
func NewPrettyHandler(out io.Writer, level slog.Leveler) *PrettyHandler {
	return &PrettyHandler{
		out:   out,
		level: level,
		mu:    &sync.Mutex{},
	}
}

var (
	_warnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("3")) // yellow
	_errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1")) // red
	_debugStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // dim
	_cyanStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // cyan
)

// Enabled reports whether the handler handles records at the given level.
func (h *PrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

// Handle writes the record's message with ANSI color based on level.
func (h *PrettyHandler) Handle(_ context.Context, r slog.Record) error {
	msg := h.prefix + r.Message

	// Append duration attribute in cyan when present.
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "duration" {
			msg += " " + _cyanStyle.Render(a.Value.String())
			return false
		}
		return true
	})

	var line string
	switch {
	case r.Level >= slog.LevelError:
		line = _errorStyle.Render(msg)
	case r.Level >= slog.LevelWarn:
		line = _warnStyle.Render(msg)
	case r.Level < slog.LevelInfo:
		line = _debugStyle.Render(msg)
	default:
		line = msg
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := lipgloss.Fprintln(h.out, line)
	return err
}

// WithAttrs returns a new handler that prepends the given attributes to messages.
func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	buf := make([]byte, 0, len(h.prefix)+len(attrs)*16)
	buf = append(buf, h.prefix...)
	for _, a := range attrs {
		buf = append(buf, a.Key...)
		buf = append(buf, '=')
		buf = append(buf, a.Value.String()...)
		buf = append(buf, ' ')
	}
	return &PrettyHandler{out: h.out, level: h.level, mu: h.mu, prefix: string(buf)}
}

// WithGroup returns a new handler that prepends the group name to messages.
func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &PrettyHandler{out: h.out, level: h.level, mu: h.mu, prefix: h.prefix + name + "."}
}

// NewLogger creates a logger for the given format and level.
// Supported formats: "pretty", "json", "text".
func NewLogger(out io.Writer, format string, level slog.Level) (*slog.Logger, error) {
	var handler slog.Handler
	switch format {
	case "json":
		handler = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level})
	case "text":
		handler = slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})
	case "pretty":
		handler = NewPrettyHandler(out, level)
	default:
		return nil, fmt.Errorf("unknown format %q: %w", format, ErrUnknownFormat)
	}
	return slog.New(handler), nil
}
