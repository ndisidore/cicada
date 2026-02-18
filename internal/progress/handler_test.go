package progress

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrettyHandler(t *testing.T) {
	t.Parallel()

	t.Run("info level prints plain message", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h)

		logger.Info("hello world")
		assert.Contains(t, buf.String(), "hello world")
		assert.Contains(t, buf.String(), "\n")
	})

	t.Run("debug filtered at info level", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h)

		logger.Debug("should not appear")
		assert.Empty(t, buf.String())
	})

	t.Run("debug visible at debug level", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelDebug)
		logger := slog.New(h)

		logger.Debug("debug visible")
		assert.Contains(t, buf.String(), "debug visible")
	})

	t.Run("error message rendered", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h)

		logger.Error("something broke")
		assert.Contains(t, buf.String(), "something broke")
	})

	t.Run("warn message rendered", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h)

		logger.Warn("watch out")
		assert.Contains(t, buf.String(), "watch out")
	})

	t.Run("inline attributes suppressed", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h)

		logger.Info("msg only", slog.String("key", "value"), slog.Int("num", 42)) //nolint:sloglint // testing that inline attrs are suppressed
		output := buf.String()
		assert.Contains(t, output, "msg only")
		assert.NotContains(t, output, "key=")
		assert.NotContains(t, output, "42")
	})

	t.Run("duration attribute rendered in output", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h)

		dur := 1500 * time.Millisecond
		logger.LogAttrs(t.Context(), slog.LevelInfo, "done step1", slog.Duration("duration", dur))
		output := buf.String()
		assert.Contains(t, output, "done step1")
		assert.Contains(t, output, "1.5s")
	})

	t.Run("Enabled respects level", func(t *testing.T) {
		t.Parallel()

		h := NewPrettyHandler(&bytes.Buffer{}, slog.LevelWarn)
		assert.False(t, h.Enabled(t.Context(), slog.LevelInfo))
		assert.True(t, h.Enabled(t.Context(), slog.LevelWarn))
		assert.True(t, h.Enabled(t.Context(), slog.LevelError))
	})

	t.Run("WithAttrs prefixes message", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h).With(slog.String("runtime", "docker")) //nolint:sloglint // testing WithAttrs propagation

		logger.Info("started")
		output := buf.String()
		assert.Contains(t, output, "runtime=docker")
		assert.Contains(t, output, "started")
	})

	t.Run("WithGroup prefixes message", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		h := NewPrettyHandler(&buf, slog.LevelInfo)
		logger := slog.New(h).WithGroup("daemon")

		logger.Info("connected")
		output := buf.String()
		assert.Contains(t, output, "daemon.")
		assert.Contains(t, output, "connected")
	})

	t.Run("WithAttrs empty is identity", func(t *testing.T) {
		t.Parallel()

		h := NewPrettyHandler(&bytes.Buffer{}, slog.LevelInfo)
		h2 := h.WithAttrs(nil)
		assert.Same(t, h, h2)
	})
}

func TestNewLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		format string
		level  slog.Level
	}{
		{name: "pretty", format: "pretty", level: slog.LevelInfo},
		{name: "json", format: "json", level: slog.LevelDebug},
		{name: "text", format: "text", level: slog.LevelWarn},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			logger, err := NewLogger(&buf, tt.format, tt.level)
			require.NoError(t, err)
			require.NotNil(t, logger)

			// Log at the configured level so the message is not filtered.
			logger.Log(t.Context(), tt.level, "test message")
			assert.Contains(t, buf.String(), "test message")
		})
	}

	t.Run("unknown format", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		logger, err := NewLogger(&buf, "josn", slog.LevelInfo)
		require.ErrorIs(t, err, ErrUnknownFormat)
		require.Nil(t, logger)
	})
}
