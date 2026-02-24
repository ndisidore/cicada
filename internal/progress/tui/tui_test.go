package tui_test

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	tuipkg "github.com/ndisidore/cicada/internal/progress/tui"
)

// newTestDisplay returns a Display configured for headless testing.
func newTestDisplay() *tuipkg.Display {
	return tuipkg.NewTest(true, []tea.ProgramOption{
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
	})
}

// requireShutdownReturns asserts that Display.Shutdown() completes within a timeout.
// synctest is not used here because bubbletea spawns OS signal-handling
// goroutines that are incompatible with synctest bubbles.
func requireShutdownReturns(t *testing.T, d *tuipkg.Display) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- d.Shutdown() }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown() did not return within timeout")
	}
}

func TestDisplay(t *testing.T) {
	t.Parallel()

	t.Run("lifecycle", func(t *testing.T) {
		t.Parallel()

		t.Run("shutdown without sends exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))
			requireShutdownReturns(t, d)
		})

		t.Run("shutdown after sends exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))

			d.Send(progressmodel.JobAddedMsg{Job: "job-1"})
			d.Send(progressmodel.JobDoneMsg{Job: "job-1"})

			requireShutdownReturns(t, d)
		})

		t.Run("shutdown before start does not panic", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Shutdown())
		})
	})

	t.Run("messages", func(t *testing.T) {
		t.Parallel()

		t.Run("skip exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))
			d.Send(progressmodel.JobSkippedMsg{Job: "deploy"})
			requireShutdownReturns(t, d)
		})

		t.Run("skip step exits cleanly", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))

			d.Send(progressmodel.JobAddedMsg{Job: "build"})
			d.Send(progressmodel.StepSkippedMsg{Job: "build", Step: "notify"})
			d.Send(progressmodel.JobDoneMsg{Job: "build"})

			requireShutdownReturns(t, d)
		})
	})

	t.Run("idempotency", func(t *testing.T) {
		t.Parallel()

		t.Run("double start does not panic", func(t *testing.T) {
			t.Parallel()

			d := newTestDisplay()
			require.NoError(t, d.Start(t.Context()))
			require.NoError(t, d.Start(t.Context()))
			requireShutdownReturns(t, d)
		})
	})
}
