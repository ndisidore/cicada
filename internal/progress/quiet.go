package progress

import (
	"context"
	"fmt"

	"github.com/moby/buildkit/client"
)

// Quiet only reports vertex errors; all other progress is suppressed.
type Quiet struct{}

// Run consumes SolveStatus events and returns the first vertex error encountered.
// It returns when ch is closed or ctx is cancelled.
func (*Quiet) Run(ctx context.Context, jobName string, ch <-chan *client.SolveStatus) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("progress display cancelled: %w", ctx.Err())
		case status, ok := <-ch:
			if !ok {
				return nil
			}
			for _, v := range status.Vertexes {
				if v.Error != "" {
					return fmt.Errorf("job %q vertex %q: %s", jobName, v.Name, v.Error)
				}
			}
		}
	}
}
