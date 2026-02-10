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
	"golang.org/x/sync/semaphore"

	"github.com/ndisidore/cicada/internal/progress"
)

// ErrNilSolver indicates that RunInput.Solver was not provided.
var ErrNilSolver = errors.New("RunInput.Solver must not be nil")

// ErrNilDisplay indicates that RunInput.Display was not provided.
var ErrNilDisplay = errors.New("RunInput.Display must not be nil")

// ErrNilDefinition indicates that a Step has a nil LLB Definition.
var ErrNilDefinition = errors.New("Step.Definition must not be nil")

// ErrUnknownDep indicates a step depends on a name not in the step list.
var ErrUnknownDep = errors.New("unknown dependency")

// ErrDuplicateStep indicates two steps share the same name.
var ErrDuplicateStep = errors.New("duplicate step name")

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

// Step pairs an LLB definition with its human-readable step name and dependencies.
type Step struct {
	Name       string
	Definition *llb.Definition
	DependsOn  []string
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
	// Parallelism limits concurrent step execution. 0 means unlimited.
	Parallelism int
}

// dagNode tracks a step and a done channel that is closed on completion.
// The err field is written before done is closed, establishing a
// happens-before for any goroutine that reads err after <-done.
type dagNode struct {
	step Step
	done chan struct{}
	err  error
}

// Run executes steps against a BuildKit daemon, respecting dependency ordering
// and parallelism limits. Steps with no dependencies start immediately (subject
// to the parallelism semaphore); steps with dependencies wait for all deps to
// complete before acquiring a semaphore slot.
func Run(ctx context.Context, in RunInput) error {
	if in.Solver == nil {
		return ErrNilSolver
	}
	if in.Display == nil {
		return ErrNilDisplay
	}
	if len(in.Steps) == 0 {
		return nil
	}

	nodes, err := buildDAG(in.Steps)
	if err != nil {
		return err
	}

	limit := int64(len(in.Steps))
	if in.Parallelism > 0 {
		limit = int64(in.Parallelism)
	}
	sem := semaphore.NewWeighted(limit)

	g, ctx := errgroup.WithContext(ctx)
	for i := range in.Steps {
		node := nodes[in.Steps[i].Name]
		g.Go(func() error {
			return runNode(ctx, node, nodes, sem, in)
		})
	}
	return g.Wait()
}

// buildDAG creates the DAG node index and validates that all deps exist.
func buildDAG(steps []Step) (map[string]*dagNode, error) {
	nodes := make(map[string]*dagNode, len(steps))
	for i := range steps {
		if _, exists := nodes[steps[i].Name]; exists {
			return nil, fmt.Errorf("step %q: %w", steps[i].Name, ErrDuplicateStep)
		}
		nodes[steps[i].Name] = &dagNode{
			step: steps[i],
			done: make(chan struct{}),
		}
	}
	for i := range steps {
		for _, dep := range steps[i].DependsOn {
			if _, ok := nodes[dep]; !ok {
				return nil, fmt.Errorf("step %q depends on %q: %w", steps[i].Name, dep, ErrUnknownDep)
			}
		}
	}
	return nodes, nil
}

// runNode waits for dependencies, acquires a semaphore slot, solves, and signals done.
func runNode(ctx context.Context, node *dagNode, nodes map[string]*dagNode, sem *semaphore.Weighted, in RunInput) error {
	defer close(node.done)

	for _, dep := range node.step.DependsOn {
		select {
		case <-nodes[dep].done:
			if nodes[dep].err != nil {
				return fmt.Errorf("step %q: dependency %q: %w", node.step.Name, dep, nodes[dep].err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if err := sem.Acquire(ctx, 1); err != nil {
		node.err = err
		return err
	}
	defer sem.Release(1)

	err := solveStep(ctx, in.Solver, in.Display, node.step.Name, node.step.Definition, in.LocalMounts)
	if err != nil {
		node.err = err
		return fmt.Errorf("step %q: %w", node.step.Name, err)
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
		err := display.Run(ctx, name, ch)
		for range ch { //nolint:revive // drain so Solve can close ch
		}
		if err != nil {
			return fmt.Errorf("displaying progress: %w", err)
		}
		return nil
	})

	return g.Wait()
}
