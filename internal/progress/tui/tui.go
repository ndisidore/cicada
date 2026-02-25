// Package tui provides an interactive terminal display adapter for BuildKit
// solve progress using bubbletea.
package tui

import (
	"context"
	"fmt"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
)

// Compile-time interface check.
var _ progressmodel.Display = (*Display)(nil)

// Display renders progress using a bubbletea interactive terminal display.
type Display struct {
	boring       bool
	p            *tea.Program
	ch           chan progressmodel.Msg
	wg           sync.WaitGroup
	once         sync.Once
	shutdownOnce sync.Once
	err          error
	opts         []tea.ProgramOption // additional program options (for testing)
}

// New returns a Display configured with the given boring mode.
func New(boring bool) *Display {
	return &Display{boring: boring, ch: make(chan progressmodel.Msg, 64)}
}

// NewTest returns a Display with extra tea.ProgramOptions for headless testing.
func NewTest(boring bool, opts []tea.ProgramOption) *Display {
	return &Display{boring: boring, opts: opts, ch: make(chan progressmodel.Msg, 64)}
}

// Start launches the bubbletea program, creates the message channel, and
// spawns a forwarding goroutine that bridges the channel to bubbletea's
// p.Send. It is idempotent; subsequent calls are no-ops.
func (t *Display) Start(ctx context.Context) error {
	t.once.Do(func() {
		m := newMultiModel(t.boring)
		opts := append([]tea.ProgramOption{tea.WithContext(ctx)}, t.opts...)
		p := tea.NewProgram(m, opts...)
		t.p = p

		// Launch bubbletea program.
		t.wg.Go(func() {
			if _, err := p.Run(); err != nil {
				t.err = fmt.Errorf("running TUI: %w", err)
			}
		})

		// Forwarding goroutine: reads from the message channel and sends
		// each message to the bubbletea program. When the channel is
		// closed (by Shutdown), sends allDoneMsg to trigger tea.Quit.
		t.wg.Go(func() {
			for msg := range t.ch {
				p.Send(msg)
			}
			p.Send(allDoneMsg{})
		})
	})
	return nil
}

// Send delivers a progress message to the display. Safe for concurrent use.
func (t *Display) Send(msg progressmodel.Msg) {
	t.ch <- msg
}

// Shutdown closes the message channel and blocks until the bubbletea
// program exits. Returns any error from the TUI runtime.
func (t *Display) Shutdown() error {
	t.shutdownOnce.Do(func() { close(t.ch) })
	t.wg.Wait()
	return t.err
}
