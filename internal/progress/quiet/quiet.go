// Package quiet provides a silent display adapter that drains BuildKit
// solve progress without output.
package quiet

import (
	"context"
	"sync"

	"github.com/ndisidore/cicada/internal/progress"
)

// Compile-time interface check.
var _ progress.Display = (*Display)(nil)

// Display drains messages silently; all progress is suppressed.
type Display struct {
	ch           chan progress.Msg
	wg           sync.WaitGroup
	once         sync.Once
	shutdownOnce sync.Once
}

// New returns a new quiet Display.
func New() *Display {
	return &Display{ch: make(chan progress.Msg, 64)}
}

// Start spawns a goroutine to drain the message channel. Idempotent.
func (q *Display) Start(_ context.Context) error {
	q.once.Do(func() {
		q.wg.Go(func() {
			//revive:disable-next-line:empty-block // intentionally draining
			for range q.ch {
			}
		})
	})
	return nil
}

// Send delivers a progress message to the display. Safe for concurrent use.
func (q *Display) Send(msg progress.Msg) {
	q.ch <- msg
}

// Shutdown closes the message channel and waits for the drain goroutine to finish.
func (q *Display) Shutdown() error {
	q.shutdownOnce.Do(func() { close(q.ch) })
	q.wg.Wait()
	return nil
}
