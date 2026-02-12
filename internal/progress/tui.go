package progress

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/moby/buildkit/client"
)

// TUI renders progress using a bubbletea interactive terminal display.
type TUI struct {
	Boring bool // use ASCII icons instead of emoji
}

// Run starts a bubbletea program that displays solve progress.
func (t *TUI) Run(ctx context.Context, jobName string, ch <-chan *client.SolveStatus) error {
	m := newModel(jobName, t.Boring)

	p := tea.NewProgram(m, tea.WithContext(ctx))

	// Forward SolveStatus events into the bubbletea event loop.
	// Selects on ctx.Done() to avoid leaking the goroutine if ch is never closed.
	go func() {
		for {
			select {
			case status, ok := <-ch:
				if !ok {
					p.Send(doneMsg{})
					return
				}
				p.Send(statusMsg{status: status})
			case <-ctx.Done():
				return
			}
		}
	}()

	final, err := p.Run()
	if err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}

	if fm, ok := final.(*model); ok && fm.err != nil {
		return fmt.Errorf("rendering progress: %w", fm.err)
	}
	return nil
}
