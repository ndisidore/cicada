package progress

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/moby/buildkit/client"
	"github.com/stretchr/testify/require"
)

// newTestTUI returns a TUI configured for headless testing.
func newTestTUI() *TUI {
	return &TUI{
		Boring: true,
		opts: []tea.ProgramOption{
			tea.WithInput(nil),
			tea.WithOutput(io.Discard),
		},
	}
}

// requireWaitReturns asserts that tui.Wait() completes within a timeout.
// synctest is not used here because bubbletea spawns OS signal-handling
// goroutines that are incompatible with synctest bubbles.
func requireWaitReturns(t *testing.T, tui *TUI) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- tui.Wait() }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() did not return within timeout")
	}
}

func TestTUI(t *testing.T) {
	t.Parallel()

	t.Run("lifecycle", func(t *testing.T) {
		t.Parallel()

		t.Run("seal without attach exits cleanly", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			require.NoError(t, tui.Start(t.Context()))
			tui.Seal()
			requireWaitReturns(t, tui)
		})

		t.Run("seal after attach exits cleanly", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			require.NoError(t, tui.Start(t.Context()))

			ch := make(chan *client.SolveStatus)
			require.NoError(t, tui.Attach(t.Context(), "job-1", ch))
			close(ch)

			tui.Seal()
			requireWaitReturns(t, tui)
		})

		t.Run("attach before start returns error", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			ch := make(chan *client.SolveStatus)
			err := tui.Attach(t.Context(), "job-1", ch)
			require.ErrorIs(t, err, ErrNotStarted)
		})

		t.Run("wait before start returns error", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			err := tui.Wait()
			require.ErrorIs(t, err, ErrNotStarted)
		})

		t.Run("seal before start does not panic", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			tui.Seal()
		})
	})

	t.Run("skip", func(t *testing.T) {
		t.Parallel()

		t.Run("skip before any attach exits cleanly", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			require.NoError(t, tui.Start(t.Context()))
			tui.Skip(t.Context(), "deploy")
			tui.Seal()
			requireWaitReturns(t, tui)
		})

		t.Run("skip before start does not panic", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			tui.Skip(t.Context(), "deploy")
		})
	})

	t.Run("skip step", func(t *testing.T) {
		t.Parallel()

		t.Run("skip step after start exits cleanly", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			require.NoError(t, tui.Start(t.Context()))

			ch := make(chan *client.SolveStatus)
			require.NoError(t, tui.Attach(t.Context(), "build", ch))
			tui.SkipStep(t.Context(), "build", "notify")
			close(ch)

			tui.Seal()
			requireWaitReturns(t, tui)
		})

		t.Run("skip step before start does not panic", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			tui.SkipStep(t.Context(), "build", "notify")
		})
	})

	t.Run("idempotency", func(t *testing.T) {
		t.Parallel()

		t.Run("double seal does not panic", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			require.NoError(t, tui.Start(t.Context()))
			tui.Seal()
			tui.Seal()
			requireWaitReturns(t, tui)
		})

		t.Run("double start does not panic", func(t *testing.T) {
			t.Parallel()

			tui := newTestTUI()
			require.NoError(t, tui.Start(t.Context()))
			require.NoError(t, tui.Start(t.Context()))
			tui.Seal()
			requireWaitReturns(t, tui)
		})
	})
}
