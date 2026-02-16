package progress

import (
	"context"
	"sync"

	"github.com/moby/buildkit/client"
)

// Quiet drains status channels silently; all progress is suppressed.
// Vertex errors are surfaced through the solver's return path, not the display.
type Quiet struct {
	wg sync.WaitGroup
}

// Start is a no-op for Quiet.
func (*Quiet) Start(_ context.Context) error { return nil }

// Attach spawns a goroutine that drains the status channel.
func (q *Quiet) Attach(ctx context.Context, _ string, ch <-chan *client.SolveStatus) error {
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

// Skip is a no-op for Quiet.
func (*Quiet) Skip(_ context.Context, _ string) {}

// SkipStep is a no-op for Quiet.
func (*Quiet) SkipStep(_ context.Context, _, _ string) {}

// Seal is a no-op for Quiet; Wait uses WaitGroup which is safe since
// all Attach calls complete before Wait is called by the caller.
func (*Quiet) Seal() {}

// Wait blocks until all attached jobs complete.
func (q *Quiet) Wait() error {
	q.wg.Wait()
	return nil
}
