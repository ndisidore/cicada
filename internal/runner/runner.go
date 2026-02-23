// Package runner executes BuildKit LLB definitions against a buildkitd daemon.
package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	rm "github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// runConfig groups shared dependencies for runNode, solveJob, solveExport, and
// related helpers, keeping function signatures under the CS-05 limit.
type runConfig struct {
	solver       rm.Solver
	rt           runtime.Runtime
	sender       progressmodel.Sender
	nodes        map[string]*dagNode
	sem          *semaphore.Weighted
	localMounts  map[string]fsutil.FS
	cacheExports []client.CacheOptionsEntry
	cacheImports []client.CacheOptionsEntry
	collector    *cache.Collector
	whenCtx      *conditional.Context
	session      []session.Attachable
	secretValues map[string]string
}

// dagNode tracks a job and a done channel that is closed on completion.
// The err field is written before done is closed, establishing a
// happens-before for any goroutine that reads err after <-done.
type dagNode struct {
	job     rm.Job
	done    chan struct{}
	err     error
	skipped bool              // true if deferred When evaluated to false
	outputs map[string]string // parsed $CICADA_OUTPUT key=value pairs
}

// Run executes jobs against a BuildKit daemon, respecting dependency ordering
// and parallelism limits. Jobs with no dependencies start immediately (subject
// to the parallelism semaphore); jobs with dependencies wait for all deps to
// complete before acquiring a semaphore slot.
//
//revive:disable-next-line:cognitive-complexity,function-length Run is a linear validate-dispatch-export-publish pipeline; splitting it hurts readability.
func Run(ctx context.Context, in rm.RunInput) error {
	if in.Solver == nil {
		return rm.ErrNilSolver
	}
	if in.Sender == nil {
		return rm.ErrNilSender
	}
	if in.Runtime == nil && slices.ContainsFunc(in.ImagePublishes, func(p rm.ImagePublish) bool { return p.ExportDocker }) {
		return rm.ErrNilRuntime
	}
	if len(in.Jobs) == 0 {
		return nil
	}

	nodes, err := buildDAG(in.Jobs)
	if err != nil {
		return err
	}

	limit := int64(len(in.Jobs))
	if in.Parallelism > 0 {
		limit = int64(in.Parallelism)
	}
	sem := semaphore.NewWeighted(limit)

	cfg := runConfig{
		solver:       in.Solver,
		rt:           in.Runtime,
		sender:       in.Sender,
		nodes:        nodes,
		sem:          sem,
		localMounts:  in.LocalMounts,
		cacheExports: in.CacheExports,
		cacheImports: in.CacheImports,
		collector:    in.CacheCollector,
		whenCtx:      in.WhenContext,
		session:      in.Session,
		secretValues: in.SecretValues,
	}

	var jobErr error
	if in.FailFast {
		g, gctx := errgroup.WithContext(ctx)
		for i := range in.Jobs {
			node := nodes[in.Jobs[i].Name]
			g.Go(func() error {
				return runNode(gctx, node, cfg)
			})
		}
		jobErr = g.Wait()
		if jobErr != nil {
			return jobErr
		}
	} else {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var errs []error
		for i := range in.Jobs {
			node := nodes[in.Jobs[i].Name]
			wg.Go(func() {
				if err := runNode(ctx, node, cfg); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		jobErr = errors.Join(errs...)
	}

	// Always filter: skipped-by-When jobs have no error but should not export.
	exports := filterSuccessful(in.Exports, nodes, func(e rm.Export) string { return e.JobName })
	imagePublishes := filterSuccessful(in.ImagePublishes, nodes, func(p rm.ImagePublish) string { return p.JobName })

	eg, ectx := errgroup.WithContext(ctx)
	for _, exp := range exports {
		eg.Go(func() error {
			if err := solveExport(ectx, exp, cfg); err != nil {
				return fmt.Errorf("exporting %q from job %q: %w", exp.Local, exp.JobName, err)
			}
			return nil
		})
	}
	exportErr := eg.Wait()

	in.ImagePublishes = imagePublishes
	publishErr := runPublishes(ctx, in, cfg)

	return errors.Join(jobErr, exportErr, publishErr)
}

// filterSuccessful returns items whose source job completed without error and
// was not skipped. Items referencing jobs not in the DAG pass through.
func filterSuccessful[T any](items []T, nodes map[string]*dagNode, jobName func(T) string) []T {
	result := make([]T, 0, len(items))
	for _, item := range items {
		node, ok := nodes[jobName(item)]
		if !ok || (node.err == nil && !node.skipped) {
			result = append(result, item)
		}
	}
	return result
}

// buildDAG creates the DAG node index and validates that all deps exist.
func buildDAG(jobs []rm.Job) (map[string]*dagNode, error) {
	nodes := make(map[string]*dagNode, len(jobs))
	for i := range jobs {
		if _, exists := nodes[jobs[i].Name]; exists {
			return nil, fmt.Errorf("job %q: %w", jobs[i].Name, pipelinemodel.ErrDuplicateJob)
		}
		nodes[jobs[i].Name] = &dagNode{
			job:  jobs[i],
			done: make(chan struct{}),
		}
	}
	for i := range jobs {
		for _, dep := range jobs[i].DependsOn {
			if _, ok := nodes[dep]; !ok {
				return nil, fmt.Errorf("job %q depends on %q: %w", jobs[i].Name, dep, pipelinemodel.ErrUnknownDep)
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
			return fmt.Errorf("job %q: %w", name, pipelinemodel.ErrCycleDetected)
		}
		state[name] = visiting
		for _, dep := range nodes[name].job.DependsOn {
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
