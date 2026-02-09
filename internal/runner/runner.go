// Package runner executes BuildKit LLB definitions against a buildkitd daemon.
package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"

	"github.com/ndisidore/ciro/internal/builder"
	"github.com/ndisidore/ciro/internal/progress"
)

// RunInput holds parameters for executing a pipeline against BuildKit.
type RunInput struct {
	// Client is the BuildKit API client used to solve LLB definitions.
	Client *client.Client
	// Result contains the LLB definitions and step names to execute.
	Result builder.Result
	// LocalMounts maps mount names to local filesystem sources.
	LocalMounts map[string]fsutil.FS
	// Display renders solve progress to the user (TUI, plain, or quiet).
	Display progress.Display
}

// Run executes each step's LLB definition sequentially against a BuildKit daemon.
func Run(ctx context.Context, in RunInput) error {
	if in.Client == nil {
		return errors.New("RunInput.Client must not be nil")
	}
	if in.Display == nil {
		return errors.New("RunInput.Display must not be nil")
	}

	if len(in.Result.Definitions) != len(in.Result.StepNames) {
		return fmt.Errorf(
			"result mismatch: %d definitions vs %d step names",
			len(in.Result.Definitions), len(in.Result.StepNames),
		)
	}

	for i, def := range in.Result.Definitions {
		name := in.Result.StepNames[i]
		if err := solveStep(ctx, in.Client, in.Display, name, def, in.LocalMounts); err != nil {
			return fmt.Errorf("step %q: %w", name, err)
		}
	}

	return nil
}

func solveStep(
	ctx context.Context,
	c *client.Client,
	display progress.Display,
	name string,
	def *llb.Definition,
	localMounts map[string]fsutil.FS,
) error {
	ch := make(chan *client.SolveStatus)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		_, err := c.Solve(ctx, def, client.SolveOpt{
			LocalMounts: localMounts,
		}, ch)
		if err != nil {
			return fmt.Errorf("solving step: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		if err := display.Run(ctx, name, ch); err != nil {
			return fmt.Errorf("displaying progress: %w", err)
		}
		return nil
	})

	return g.Wait()
}
