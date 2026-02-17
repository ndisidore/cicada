// Package quiet provides a silent display adapter that drains BuildKit
// solve progress without output.
package quiet

import (
	"context"
	"sync"
	"time"

	"github.com/moby/buildkit/client"

	"github.com/ndisidore/cicada/internal/progress"
)

// Compile-time interface check.
var _ progress.Display = (*Display)(nil)

// Display drains status channels silently; all progress is suppressed.
// Vertex errors are surfaced through the solver's return path, not the display.
type Display struct {
	wg sync.WaitGroup
}

// New returns a new quiet Display.
func New() *Display {
	return &Display{}
}

// Start is a no-op for Display.
func (*Display) Start(_ context.Context) error { return nil }

// Attach spawns a goroutine that drains the status channel.
func (q *Display) Attach(ctx context.Context, _ string, ch <-chan *client.SolveStatus, _ map[string]time.Duration) error {
	q.wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				//revive:disable-next-line:empty-block // intentionally draining
				for range ch {
				}
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
			}
		}
	})
	return nil
}

// Skip is a no-op for Display.
func (*Display) Skip(_ context.Context, _ string) {}

// SkipStep is a no-op for Display.
func (*Display) SkipStep(_ context.Context, _, _ string) {}

// Retry is a no-op for Display.
func (*Display) Retry(_ context.Context, _ string, _, _ int, _ error) {}

// Timeout is a no-op for Display.
func (*Display) Timeout(_ context.Context, _ string, _ time.Duration) {}

// Seal is a no-op for Display; Wait uses WaitGroup which is safe since
// all Attach calls complete before Wait is called by the caller.
func (*Display) Seal() {}

// Wait blocks until all attached jobs complete.
func (q *Display) Wait() error {
	q.wg.Wait()
	return nil
}
