package tui_test

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/moby/buildkit/client"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/internal/progress"
	tuipkg "github.com/ndisidore/cicada/internal/progress/tui"
)

// newTestDisplay returns a Display configured for headless testing.
func newTestDisplay() *tuipkg.Display {
	return tuipkg.NewTest(true, []tea.ProgramOption{
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	})
}

// requireWaitReturns asserts that Display.Wait() completes within a timeout.
// synctest is not used here because bubbletea spawns OS signal-handling
// goroutines that are incompatible with synctest bubbles.
func requireWaitReturns(t *testing.T, d *tuipkg.Display) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- d.Wait() }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() did not return within timeout")
	}
}

func TestDisplay(t *testing.T) {
	t.Parallel()

	t.Run("lifecycle", func(t *testing.T) {
		t.Parallel()

		t.Run("seal without attach exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))
			d.Seal()
			requireWaitReturns(t, d)
		})

		t.Run("seal after attach exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))

			ch := make(chan *client.SolveStatus)
			require.NoError(t, d.Attach(t.Context(), "job-1", ch, nil))
			close(ch)

			d.Seal()
			requireWaitReturns(t, d)
		})

		t.Run("attach before start returns error", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			ch := make(chan *client.SolveStatus)
			err := d.Attach(t.Context(), "job-1", ch, nil)
			require.ErrorIs(t, err, progress.ErrNotStarted)
		})

		t.Run("wait before start returns error", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			err := d.Wait()
			require.ErrorIs(t, err, progress.ErrNotStarted)
		})

		t.Run("seal before start does not panic", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			d.Seal()
		})
	})

	t.Run("skip", func(t *testing.T) {
		t.Parallel()

		t.Run("skip before any attach exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))
			d.Skip(t.Context(), "deploy")
			d.Seal()
			requireWaitReturns(t, d)
		})

		t.Run("skip before start does not panic", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			d.Skip(t.Context(), "deploy")
		})
	})

	t.Run("skip step", func(t *testing.T) {
		t.Parallel()

		t.Run("skip step after start exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))

			ch := make(chan *client.SolveStatus)
			require.NoError(t, d.Attach(t.Context(), "build", ch, nil))
			d.SkipStep(t.Context(), "build", "notify")
			close(ch)

			d.Seal()
			requireWaitReturns(t, d)
		})

		t.Run("skip step before start does not panic", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			d.SkipStep(t.Context(), "build", "notify")
		})
	})

	t.Run("idempotency", func(t *testing.T) {
		t.Parallel()

		t.Run("double seal does not panic", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))
			d.Seal()
			d.Seal()
			requireWaitReturns(t, d)
		})

		t.Run("double start does not panic", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))
			require.NoError(t, d.Start(t.Context()))
			d.Seal()
			requireWaitReturns(t, d)
		})
	})
}
