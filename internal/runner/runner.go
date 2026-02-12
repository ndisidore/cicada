// Package runner executes BuildKit LLB definitions against a buildkitd daemon.
package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/ndisidore/cicada/internal/builder"
	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/progress"
	"github.com/ndisidore/cicada/pkg/pipeline"
)

// ErrNilSolver indicates that Solver was not provided.
var ErrNilSolver = errors.New("solver must not be nil")

// ErrNilDisplay indicates that Display was not provided.
var ErrNilDisplay = errors.New("display must not be nil")

// ErrNilDefinition indicates that an LLB Definition is unexpectedly nil.
var ErrNilDefinition = errors.New("definition must not be nil")

// ErrUnknownDep indicates a step depends on a name not in the step list.
var ErrUnknownDep = errors.New("unknown dependency")

// ErrDuplicateStep indicates two steps share the same name.
var ErrDuplicateStep = errors.New("duplicate step name")

// ErrCycleDetected indicates a dependency cycle exists among steps.
var ErrCycleDetected = errors.New("dependency cycle detected")

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
	// Exports contains artifacts to export to the host after all steps complete.
	Exports []builder.LocalExport
	// CacheExports configures cache export destinations (e.g. registry, gha, local).
	CacheExports []client.CacheOptionsEntry
	// CacheImports configures cache import sources.
	CacheImports []client.CacheOptionsEntry
	// CacheCollector accumulates vertex stats for cache analytics; nil disables.
	CacheCollector *cache.Collector
}

// solveConfig groups parameters for solveStep and solveExport, keeping their
// signatures under the CS-05 limit.
type solveConfig struct {
	localMounts  map[string]fsutil.FS
	cacheExports []client.CacheOptionsEntry
	cacheImports []client.CacheOptionsEntry
	collector    *cache.Collector
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

	cfg := solveConfig{
		localMounts:  in.LocalMounts,
		cacheExports: in.CacheExports,
		cacheImports: in.CacheImports,
		collector:    in.CacheCollector,
	}

	g, gctx := errgroup.WithContext(ctx)
	for i := range in.Steps {
		node := nodes[in.Steps[i].Name]
		g.Go(func() error {
			return runNode(gctx, node, nodes, sem, in.Solver, in.Display, cfg)
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Export artifacts to the host filesystem concurrently.
	// Uses the original ctx since the step errgroup's derived context is
	// canceled when Wait returns.
	eg, ectx := errgroup.WithContext(ctx)
	for _, exp := range in.Exports {
		eg.Go(func() error {
			if err := solveExport(ectx, in.Solver, in.Display, exp, cfg); err != nil {
				return fmt.Errorf("exporting %q from step %q: %w", exp.Local, exp.StepName, err)
			}
			return nil
		})
	}
	return eg.Wait()
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
	if err := detectCycles(nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// detectCycles uses a 3-state DFS to find dependency cycles in the DAG.
func detectCycles(nodes map[string]*dagNode) error {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(nodes))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case visited:
			return nil
		case visiting:
			return fmt.Errorf("step %q: %w", name, ErrCycleDetected)
		}
		state[name] = visiting
		for _, dep := range nodes[name].step.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}
	for name := range nodes {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// runNode waits for dependencies, acquires a semaphore slot, solves, and signals done.
func runNode(ctx context.Context, node *dagNode, nodes map[string]*dagNode, sem *semaphore.Weighted, solver Solver, display progress.Display, cfg solveConfig) error {
	defer close(node.done)

	for _, dep := range node.step.DependsOn {
		select {
		case <-nodes[dep].done:
			if nodes[dep].err != nil {
				node.err = fmt.Errorf("dependency %q: %w", dep, nodes[dep].err)
				return fmt.Errorf("step %q: %w", node.step.Name, node.err)
			}
		case <-ctx.Done():
			node.err = ctx.Err()
			return node.err
		}
	}

	if err := sem.Acquire(ctx, 1); err != nil {
		node.err = err
		return err
	}
	defer sem.Release(1)

	err := solveStep(ctx, solver, display, node.step.Name, node.step.Definition, cfg)
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
	cfg solveConfig,
) error {
	if def == nil {
		return ErrNilDefinition
	}

	ch := make(chan *client.SolveStatus)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		_, err := s.Solve(ctx, def, client.SolveOpt{
			LocalMounts:  cfg.localMounts,
			CacheExports: cfg.cacheExports,
			CacheImports: cfg.cacheImports,
		}, ch)
		if err != nil {
			return fmt.Errorf("solving step: %w", err)
		}
		return nil
	})

	g.Go(func() error {
		displayCh := teeStatus(ctx, ch, cfg.collector, name)
		err := display.Run(ctx, name, displayCh)
		for range displayCh { //nolint:revive // drain so Solve can close ch
		}
		if err != nil {
			return fmt.Errorf("displaying progress: %w", err)
		}
		return nil
	})

	return g.Wait()
}

// solveExport solves an export LLB definition using the local exporter to
// write files to the host filesystem.
func solveExport(ctx context.Context, s Solver, display progress.Display, exp builder.LocalExport, cfg solveConfig) error {
	if exp.Definition == nil {
		return ErrNilDefinition
	}
	if exp.Local == "" {
		return pipeline.ErrEmptyExportLocal
	}
	outputDir := filepath.Dir(exp.Local)
	if exp.Dir {
		outputDir = exp.Local
	}

	ch := make(chan *client.SolveStatus)
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		_, err := s.Solve(ctx, exp.Definition, client.SolveOpt{
			Exports: []client.ExportEntry{{
				Type:      client.ExporterLocal,
				OutputDir: outputDir,
			}},
			CacheExports: cfg.cacheExports,
			CacheImports: cfg.cacheImports,
		}, ch)
		if err != nil {
			return fmt.Errorf("solving export: %w", err)
		}
		return nil
	})

	displayName := "export:" + exp.StepName
	g.Go(func() error {
		displayCh := teeStatus(ctx, ch, cfg.collector, displayName)
		err := display.Run(ctx, displayName, displayCh)
		for range displayCh { //nolint:revive // drain so Solve can close ch
		}
		if err != nil {
			return fmt.Errorf("displaying export progress: %w", err)
		}
		return nil
	})

	return g.Wait()
}

// drainStatus discards remaining items from ch so the sender is not blocked.
func drainStatus(ch <-chan *client.SolveStatus) {
	//nolint:revive // intentionally discarding remaining events
	for range ch {
	}
}

// teeStatus interposes a Collector between the source status channel and the
// display consumer. If collector is nil, returns src directly (zero overhead).
// On context cancellation the goroutine drains src so the Solve sender can exit.
func teeStatus(ctx context.Context, src <-chan *client.SolveStatus, collector *cache.Collector, stepName string) <-chan *client.SolveStatus {
	if collector == nil {
		return src
	}
	out := make(chan *client.SolveStatus)
	go func() {
		defer close(out)
		defer drainStatus(src)
		for {
			select {
			case status, ok := <-src:
				if !ok {
					return
				}
				collector.Observe(stepName, status)
				select {
				case out <- status:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
