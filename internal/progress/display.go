// Package progress provides display adapters for BuildKit solve progress.
package progress

import (
	"context"

	"github.com/moby/buildkit/client"
)

// Display renders BuildKit solve progress to the user.
type Display interface {
	// Run consumes SolveStatus events for a step and renders them.
	// It returns when ch is closed or ctx is cancelled.
	Run(ctx context.Context, stepName string, ch <-chan *client.SolveStatus) error
}
