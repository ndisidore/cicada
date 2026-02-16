// Package progress provides display adapters for BuildKit solve progress.
package progress

import (
	"context"
	"errors"

	"github.com/moby/buildkit/client"
)

// ErrNotStarted indicates Attach was called before Start.
var ErrNotStarted = errors.New("display not started")

// Display renders BuildKit solve progress to the user.
type Display interface {
	// Start initializes the display (e.g. launches a TUI).
	Start(ctx context.Context) error
	// Attach registers a job's status channel and spawns a consumer goroutine.
	// May be called concurrently from multiple goroutines. The caller must
	// ensure ch is closed promptly (including on context cancellation) so the
	// consumer goroutine can exit.
	Attach(ctx context.Context, jobName string, ch <-chan *client.SolveStatus) error
	// Skip reports that a job was skipped due to a when condition.
	// Called for both static (pre-build) and deferred (runtime) skips.
	Skip(ctx context.Context, jobName string)
	// SkipStep reports that a step within a job was skipped due to a when condition.
	SkipStep(ctx context.Context, jobName, stepName string)
	// Seal signals that no more Attach calls will be made. Must be called
	// before Wait to prevent premature completion detection.
	Seal()
	// Wait blocks until all attached jobs complete and returns any error encountered.
	// Seal must be called before Wait.
	Wait() error
}
