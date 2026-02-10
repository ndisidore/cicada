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

	"github.com/ndisidore/cicada/internal/progress"
)

// ErrNilSolver indicates that RunInput.Solver was not provided.
var ErrNilSolver = errors.New("RunInput.Solver must not be nil")

// ErrNilDisplay indicates that RunInput.Display was not provided.
var ErrNilDisplay = errors.New("RunInput.Display must not be nil")

// ErrNilDefinition indicates that a Step has a nil LLB Definition.
var ErrNilDefinition = errors.New("Step.Definition must not be nil")

// Solver abstracts the BuildKit Solve RPC for testability.
//
// Channel close contract: the status channel passed to Solve is owned by the caller
// of Solve (e.g. solveStep) until the implementer closes it. Implementations of Solver
// MUST close the provided status channel when Solve returns or completes, so that
// consumers such as solveStep and display.Run do not hang.
type Solver interface {
	// Solve runs the LLB definition. The implementer must close statusChan when
	// Solve returns or completes; ownership of the channel remains with the
	// caller of Solve until it is closed.
	Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error)
}

// Step pairs an LLB definition with its human-readable step name.
type Step struct {
	Name       string
	Definition *llb.Definition
}

// RunInput holds parameters for executing a pipeline against BuildKit.
type RunInput struct {
	// Solver is the BuildKit API client used to solve LLB definitions.
	Solver Solver
	// Steps contains the LLB definitions and step names to execute.
	Steps []Step
	// LocalMounts maps mount names to local filesystem sources.
	LocalMounts map[string]fsutil.FS
	// Display renders solve progress to the user (TUI, plain, or quiet).
	Display progress.Display
}

// Run executes each step's LLB definition sequentially against a BuildKit daemon.
func Run(ctx context.Context, in RunInput) error {
	if in.Solver == nil {
		return ErrNilSolver
	}
	if in.Display == nil {
		return ErrNilDisplay
	}

	for _, step := range in.Steps {
		if err := solveStep(ctx, in.Solver, in.Display, step.Name, step.Definition, in.LocalMounts); err != nil {
			return fmt.Errorf("step %q: %w", step.Name, err)
		}
	}

	return nil
}

func solveStep(
	ctx context.Context,
	s Solver,
	display progress.Display,
	name string,
	def *llb.Definition,
	localMounts map[string]fsutil.FS,
) error {
	if def == nil {
		return ErrNilDefinition
	}

	ch := make(chan *client.SolveStatus)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		_, err := s.Solve(ctx, def, client.SolveOpt{
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
