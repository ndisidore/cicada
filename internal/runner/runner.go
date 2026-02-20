// Package runner executes BuildKit LLB definitions against a buildkitd daemon.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/session"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"

	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/progress"
	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/pipeline"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// ErrNilSolver indicates that Solver was not provided.
var ErrNilSolver = errors.New("solver must not be nil")

// ErrNilSender indicates that Sender was not provided.
var ErrNilSender = errors.New("sender must not be nil")

// ErrNilDefinition indicates that an LLB Definition is unexpectedly nil.
var ErrNilDefinition = errors.New("definition must not be nil")

// ErrNilRuntime indicates that Runtime was not provided but export-docker requires it.
var ErrNilRuntime = errors.New("runtime must not be nil for export-docker")

// ErrJobTimeout indicates a job exceeded its configured timeout.
var ErrJobTimeout = errors.New("job timeout")

// ErrStepTimeout indicates a step exceeded its configured timeout.
var ErrStepTimeout = errors.New("step timeout")

// JobTimeoutError provides structured detail about a job timeout.
type JobTimeoutError struct {
	JobName string
	Timeout time.Duration
}

func (e *JobTimeoutError) Error() string {
	return fmt.Sprintf("job %q exceeded %s timeout", e.JobName, e.Timeout)
}

func (*JobTimeoutError) Unwrap() error { return ErrJobTimeout }

// StepTimeoutError provides structured detail about a step timeout.
type StepTimeoutError struct {
	JobName  string
	StepName string
	Timeout  time.Duration
}

func (e *StepTimeoutError) Error() string {
	return fmt.Sprintf("step %q in job %q exceeded %s timeout", e.StepName, e.JobName, e.Timeout)
}

func (*StepTimeoutError) Unwrap() error { return ErrStepTimeout }

// ErrExportDockerMultiPlatform indicates export-docker was set on a multi-platform publish.
var ErrExportDockerMultiPlatform = errors.New("export-docker is not supported for multi-platform publishes")

// ErrPublishSettingConflict indicates variants targeting the same image have inconsistent settings.
var ErrPublishSettingConflict = errors.New("conflicting publish settings for same image")

// ErrDuplicatePlatform indicates two variants in a multi-platform publish target the same platform.
var ErrDuplicatePlatform = errors.New("duplicate platform in multi-platform publish")

// Solver abstracts the BuildKit Solve RPC for testability.
//
// Channel close contract: the status channel passed to Solve is owned by the caller
// of Solve (e.g. solveJob) until the implementer closes it. Implementations of Solver
// MUST close the provided status channel when Solve returns or completes, so that
// consumers such as solveJob and its bridge goroutines do not hang.
type Solver interface {
	// Solve runs the LLB definition. The implementer must close statusChan when
	// Solve returns or completes; ownership of the channel remains with the
	// caller of Solve until it is closed.
	Solve(ctx context.Context, def *llb.Definition, opt client.SolveOpt, statusChan chan *client.SolveStatus) (*client.SolveResponse, error)
	// Build runs a gateway build function (used for multi-platform manifest assembly).
	Build(ctx context.Context, opt client.SolveOpt, product string, buildFunc gateway.BuildFunc, statusChan chan *client.SolveStatus) (*client.SolveResponse, error)
}

// DeferredEvaluator evaluates a deferred when-condition after dependency
// outputs become available. *conditional.When satisfies this interface.
// A skipped dependency propagates skip status to all dependents, so
// EvaluateDeferred is never called with missing output data from skipped jobs.
type DeferredEvaluator interface {
	EvaluateDeferred(ctx conditional.Context, depOutputs map[string]map[string]string) (bool, error)
}

// Job pairs an LLB definition with its human-readable job name and dependencies.
type Job struct {
	Name         string
	Definition   *llb.Definition
	DependsOn    []string
	When         DeferredEvaluator           // deferred condition; nil = always run
	Env          map[string]string           // pipeline-scoped env for deferred condition evaluation
	Matrix       map[string]string           // matrix dimension values for deferred condition evaluation
	OutputDef    *llb.Definition             // extracts /cicada/output for deferred conditions
	SkippedSteps []string                    // step names skipped by static when conditions
	Timeout      time.Duration               // job-level timeout; 0 = no timeout
	Retry        *pipeline.Retry             // job-level retry config; nil = no retry
	StepTimeouts map[string]time.Duration    // vertex name -> configured timeout; nil = no step timeouts
	CmdInfos     map[string]progress.CmdInfo // vertex name -> command metadata
	Steps        []StepExec                  // per-step execution; nil = monolithic solve
	BaseState    llb.State                   // pre-step LLB state; set when Steps is non-nil
}

// StepExec describes a single step for gateway-based step-controlled execution.
type StepExec struct {
	Name         string
	Retry        *pipeline.Retry
	AllowFailure bool
	Timeouts     map[string]time.Duration
	CmdInfos     map[string]progress.CmdInfo // vertex name -> command metadata
	// Build produces the LLB state for this step given a base state and retry attempt.
	// retryAttempt is 0 for the first try; >0 triggers cache busting.
	Build func(base llb.State, retryAttempt int) (llb.State, error)
}

// Export pairs an LLB definition containing exported files with the
// target host path for the local exporter.
type Export struct {
	Definition *llb.Definition
	JobName    string
	Local      string // host path target
	Dir        bool   // true when exporting a directory (trailing / on container path)
}

// ImagePublish pairs an LLB definition with image publishing metadata.
type ImagePublish struct {
	Definition   *llb.Definition
	JobName      string
	Image        string
	Push         bool
	Insecure     bool
	ExportDocker bool
	Platform     string
}

// RunInput holds parameters for executing a pipeline against BuildKit.
type RunInput struct {
	// Solver is the BuildKit API client used to solve LLB definitions.
	Solver Solver
	// Runtime is the container runtime used for image loading (export-docker).
	Runtime runtime.Runtime
	// Jobs contains the LLB definitions and job names to execute.
	Jobs []Job
	// LocalMounts maps mount names to local filesystem sources.
	LocalMounts map[string]fsutil.FS
	// Sender delivers progress messages to the display.
	Sender progress.Sender
	// Parallelism limits concurrent job execution. 0 means unlimited.
	Parallelism int
	// FailFast cancels all running jobs on first failure when true. When
	// false, independent jobs continue and only the failed job's dependents
	// are skipped.
	FailFast bool
	// Exports contains artifacts to export to the host after all jobs complete.
	Exports []Export
	// ImagePublishes contains image publish targets to solve after all jobs complete.
	ImagePublishes []ImagePublish
	// CacheExports configures cache export destinations (e.g. registry, gha, local).
	CacheExports []client.CacheOptionsEntry
	// CacheImports configures cache import sources.
	CacheImports []client.CacheOptionsEntry
	// CacheCollector accumulates vertex stats for cache analytics; nil disables.
	CacheCollector *cache.Collector
	// WhenContext provides static bindings for deferred When evaluation.
	WhenContext *conditional.Context
	// Session provides secret attachables for BuildKit gRPC session.
	Session []session.Attachable
	// SecretValues maps secret names to plaintext values for log redaction.
	SecretValues map[string]string
}

// runConfig groups shared dependencies for runNode, solveJob, solveExport, and
// related helpers, keeping function signatures under the CS-05 limit.
type runConfig struct {
	solver       Solver
	rt           runtime.Runtime
	sender       progress.Sender
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
	job     Job
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
func Run(ctx context.Context, in RunInput) error {
	if in.Solver == nil {
		return ErrNilSolver
	}
	if in.Sender == nil {
		return ErrNilSender
	}
	if in.Runtime == nil && slices.ContainsFunc(in.ImagePublishes, func(p ImagePublish) bool { return p.ExportDocker }) {
		return ErrNilRuntime
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

	exports := in.Exports
	imagePublishes := in.ImagePublishes
	if jobErr != nil {
		exports = filterSuccessful(in.Exports, nodes, func(e Export) string { return e.JobName })
		imagePublishes = filterSuccessful(in.ImagePublishes, nodes, func(p ImagePublish) string { return p.JobName })
	}

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

// filterSuccessful returns items whose source job completed without error and was not skipped.
func filterSuccessful[T any](items []T, nodes map[string]*dagNode, jobName func(T) string) []T {
	result := make([]T, 0, len(items))
	for _, item := range items {
		node := nodes[jobName(item)]
		if node != nil && node.err == nil && !node.skipped {
			result = append(result, item)
		}
	}
	return result
}

// buildDAG creates the DAG node index and validates that all deps exist.
func buildDAG(jobs []Job) (map[string]*dagNode, error) {
	nodes := make(map[string]*dagNode, len(jobs))
	for i := range jobs {
		if _, exists := nodes[jobs[i].Name]; exists {
			return nil, fmt.Errorf("job %q: %w", jobs[i].Name, pipeline.ErrDuplicateJob)
		}
		nodes[jobs[i].Name] = &dagNode{
			job:  jobs[i],
			done: make(chan struct{}),
		}
	}
	for i := range jobs {
		for _, dep := range jobs[i].DependsOn {
			if _, ok := nodes[dep]; !ok {
				return nil, fmt.Errorf("job %q depends on %q: %w", jobs[i].Name, dep, pipeline.ErrUnknownDep)
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
			return fmt.Errorf("job %q: %w", name, pipeline.ErrCycleDetected)
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

// runNode waits for dependencies, acquires a semaphore slot, solves, and signals done.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic,function-length runNode is a linear pipeline of wait-evaluate-solve-extract; splitting it hurts readability.
func runNode(ctx context.Context, node *dagNode, cfg runConfig) error {
	defer close(node.done)

	// Wait for all dependencies. A failed or skipped dependency prevents
	// this node from running: errors propagate as errors, skips propagate
	// as skips so downstream nodes never see missing output data.
	for _, dep := range node.job.DependsOn {
		select {
		case <-cfg.nodes[dep].done:
			if cfg.nodes[dep].err != nil {
				node.err = fmt.Errorf("dependency %q: %w", dep, cfg.nodes[dep].err)
				return fmt.Errorf("job %q: %w", node.job.Name, node.err)
			}
			if cfg.nodes[dep].skipped {
				node.skipped = true
				cfg.sender.Send(progress.JobSkippedMsg{Job: node.job.Name})
				return nil
			}
		case <-ctx.Done():
			node.err = ctx.Err()
			return node.err
		}
	}

	// Evaluate deferred when condition after deps complete.
	if node.job.When != nil && cfg.whenCtx == nil {
		slogctx.FromContext(ctx).Warn("deferred when condition present but no WhenContext provided; running unconditionally",
			slog.String("job", node.job.Name),
		)
	}
	if node.job.When != nil && cfg.whenCtx != nil {
		depOutputs := collectDepOutputs(cfg.nodes, node.job.DependsOn)
		whenCtx := *cfg.whenCtx
		whenCtx.PipelineEnv = node.job.Env
		whenCtx.Matrix = node.job.Matrix
		result, err := node.job.When.EvaluateDeferred(whenCtx, depOutputs)
		if err != nil {
			node.err = err
			return fmt.Errorf("job %q when: %w", node.job.Name, err)
		}
		if !result {
			node.skipped = true
			cfg.sender.Send(progress.JobSkippedMsg{Job: node.job.Name})
			return nil
		}
	}

	if err := cfg.sem.Acquire(ctx, 1); err != nil {
		node.err = err
		return err
	}
	defer cfg.sem.Release(1)

	// Apply job-level timeout.
	solveCtx := ctx
	if node.job.Timeout > 0 {
		var cancel context.CancelFunc
		solveCtx, cancel = context.WithTimeoutCause(ctx, node.job.Timeout,
			&JobTimeoutError{JobName: node.job.Name, Timeout: node.job.Timeout})
		defer cancel()
	}

	err := retryJob(solveCtx, node, cfg)
	if err != nil {
		if cause := context.Cause(solveCtx); errors.Is(cause, ErrJobTimeout) {
			cfg.sender.Send(progress.JobTimeoutMsg{Job: node.job.Name, Timeout: node.job.Timeout})
			node.err = progress.NewDisplayError(
				fmt.Sprintf("job %q timed out after %s", node.job.Name, node.job.Timeout),
				cause,
			)
			return node.err
		}
		if len(node.job.StepTimeouts) > 0 {
			err = cleanTimeoutError(err, node.job.Name, node.job.StepTimeouts)
		}
		var exitErr *gatewaypb.ExitError
		if errors.As(err, &exitErr) {
			node.err = progress.NewDisplayError(
				fmt.Sprintf("job %q: exit code: %d", node.job.Name, exitErr.ExitCode),
				err,
			)
			return node.err
		}
		node.err = err
		return fmt.Errorf("job %q: %w", node.job.Name, err)
	}

	for _, step := range node.job.SkippedSteps {
		cfg.sender.Send(progress.StepSkippedMsg{Job: node.job.Name, Step: step})
	}

	// Extract outputs for downstream deferred conditions.
	// Step-controlled jobs extract outputs inside the gateway BuildFunc.
	if node.outputs == nil && node.job.OutputDef != nil {
		node.outputs, err = extractOutputs(ctx, node.job.OutputDef, cfg)
		if err != nil {
			node.err = err
			return fmt.Errorf("job %q output extraction: %w", node.job.Name, err)
		}
	}
	return nil
}

// retryJob executes solveJob with optional retry logic. If no retry config is
// set, a single attempt is made. Otherwise, up to 1 + retry.Attempts attempts
// are made with configurable delay and backoff between retries.
//
//revive:disable-next-line:cognitive-complexity retryJob is a linear retry loop; splitting it hurts readability.
func retryJob(ctx context.Context, node *dagNode, cfg runConfig) error {
	// Dispatch to step-controlled or monolithic solve.
	solve := func() error {
		if node.job.Steps != nil {
			return solveJobSteps(ctx, node, cfg)
		}
		return solveJob(ctx, solveJobInput{
			name:         node.job.Name,
			def:          node.job.Definition,
			cfg:          cfg,
			stepTimeouts: node.job.StepTimeouts,
			cmdInfos:     node.job.CmdInfos,
		})
	}

	r := node.job.Retry
	if r == nil {
		return solve()
	}

	maxAttempts := 1 + r.Attempts
	var lastErr error
	for attempt := range maxAttempts {
		lastErr = solve()
		if lastErr == nil {
			return nil
		}

		// Don't sleep after the final attempt.
		if attempt == maxAttempts-1 {
			break
		}

		// Bail immediately if the context was cancelled (e.g. job timeout).
		if ctx.Err() != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return ctx.Err()
		}

		cfg.sender.Send(progress.JobRetryMsg{
			Job: node.job.Name, Attempt: attempt + 2,
			MaxAttempts: maxAttempts, Err: lastErr,
		})

		delay := retryDelay(r, attempt)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				if cause := context.Cause(ctx); cause != nil {
					return cause
				}
				return ctx.Err()
			}
		}
	}
	return lastErr
}

// solveJobSteps uses the gateway API to execute a job's steps individually,
// enabling per-step retry and allow-failure.
func solveJobSteps(ctx context.Context, node *dagNode, cfg runConfig) error {
	// Merge all step timeouts and cmd infos into single maps for JobAddedMsg.
	var merged map[string]time.Duration
	var mergedCmds map[string]progress.CmdInfo
	for _, step := range node.job.Steps {
		if step.Timeouts != nil {
			if merged == nil {
				merged = make(map[string]time.Duration)
			}
			maps.Copy(merged, step.Timeouts)
		}
		if step.CmdInfos != nil {
			if mergedCmds == nil {
				mergedCmds = make(map[string]progress.CmdInfo)
			}
			maps.Copy(mergedCmds, step.CmdInfos)
		}
	}

	ch := make(chan *client.SolveStatus)
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, node.job.Name)

	cfg.sender.Send(progress.JobAddedMsg{Job: node.job.Name, StepTimeouts: merged, CmdInfos: mergedCmds})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, cfg.sender, node.job.Name, displayCh))

	stepOutputs := make(map[string]string)
	_, err := cfg.solver.Build(ctx, client.SolveOpt{
		LocalMounts:  cfg.localMounts,
		CacheExports: cfg.cacheExports,
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, "", stepControlBuildFunc(node, cfg, &stepOutputs), ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving job steps: %w", err)
	}
	// Store outputs extracted inside the gateway for deferred conditions.
	node.outputs = stepOutputs
	return nil
}

// stepControlBuildFunc returns a gateway.BuildFunc that executes steps individually
// with per-step retry and allow-failure support.
func stepControlBuildFunc(node *dagNode, cfg runConfig, outputs *map[string]string) gateway.BuildFunc {
	return func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		currentState := node.job.BaseState
		var lastRef gateway.Reference

		for _, step := range node.job.Steps {
			stepState, ref, err := solveStep(ctx, solveStepInput{
				client: c, cfg: cfg,
				jobName: node.job.Name, step: step, base: currentState,
			})
			if err != nil {
				if step.AllowFailure {
					cfg.sender.Send(progress.StepAllowedFailureMsg{
						Job: node.job.Name, Step: step.Name, Err: err,
					})
					// Leave currentState unchanged: a failed AllowFailure
					// step may have tainted the filesystem, so subsequent
					// steps must build from the last known-good state.
					continue
				}
				return nil, err
			}
			currentState = stepState
			lastRef = ref
		}

		if lastRef != nil {
			if extracted := extractRefOutputs(ctx, lastRef, node.job.Name); extracted != nil {
				*outputs = extracted
			}
		}

		res := gateway.NewResult()
		if lastRef != nil {
			res.SetRef(lastRef)
		}
		return res, nil
	}
}

// extractRefOutputs reads /cicada/output from a gateway reference and parses
// KEY=VALUE lines. Returns an empty map when the file does not exist.
// ReadFile returns gRPC status errors, so errgrpc.ToNative converts them
// before errdefs checks.
func extractRefOutputs(ctx context.Context, ref gateway.Reference, jobName string) map[string]string {
	data, err := ref.ReadFile(ctx, gateway.ReadRequest{Filename: "cicada/output"})
	if err != nil {
		err = errgrpc.ToNative(err)
	}
	switch {
	case err == nil:
		return parseOutputLines(string(data))
	case errdefs.IsNotFound(err):
		return map[string]string{}
	default:
		slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelWarn, "reading step output file",
			slog.String("job", jobName),
			slog.String("file", "cicada/output"),
			slog.String("error", err.Error()))
		return nil
	}
}

// solveStepInput bundles parameters for solveStep (CS-05).
type solveStepInput struct {
	client  gateway.Client
	cfg     runConfig
	jobName string
	step    StepExec
	base    llb.State
}

// solveStep executes a single step with optional retry logic via the gateway client.
//
//revive:disable-next-line:cognitive-complexity solveStep is a linear retry loop; splitting it hurts readability.
func solveStep(ctx context.Context, in solveStepInput) (llb.State, gateway.Reference, error) {
	maxAttempts := 1
	if in.step.Retry != nil {
		maxAttempts = 1 + in.step.Retry.Attempts
	}

	var lastErr error
	for attempt := range maxAttempts {
		stepState, err := in.step.Build(in.base, attempt)
		if err != nil {
			return llb.State{}, nil, fmt.Errorf("building step %q: %w", in.step.Name, err)
		}

		def, err := stepState.Marshal(ctx)
		if err != nil {
			return llb.State{}, nil, fmt.Errorf("marshaling step %q: %w", in.step.Name, err)
		}

		result, err := in.client.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
			Evaluate:   true,
		})
		if err == nil {
			ref, refErr := result.SingleRef()
			if refErr != nil {
				return llb.State{}, nil, fmt.Errorf("step %q: getting ref: %w", in.step.Name, refErr)
			}
			return stepState, ref, nil
		}

		lastErr = fmt.Errorf("solving step %q: %w", in.step.Name, err)
		if attempt == maxAttempts-1 {
			break
		}

		if ctx.Err() != nil {
			if cause := context.Cause(ctx); cause != nil {
				return llb.State{}, nil, cause
			}
			return llb.State{}, nil, ctx.Err()
		}

		in.cfg.sender.Send(progress.StepRetryMsg{
			Job: in.jobName, Step: in.step.Name,
			Attempt: attempt + 2, MaxAttempts: maxAttempts,
			Err: lastErr,
		})

		delay := retryDelay(in.step.Retry, attempt)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				if cause := context.Cause(ctx); cause != nil {
					return llb.State{}, nil, cause
				}
				return llb.State{}, nil, ctx.Err()
			}
		}
	}
	return llb.State{}, nil, lastErr
}

// _maxDelay caps retry delays to prevent overflow.
const _maxDelay = time.Duration(math.MaxInt64)

// retryDelay computes the delay for a given retry attempt based on the backoff strategy.
// Results are capped at math.MaxInt64 nanoseconds to prevent overflow.
func retryDelay(r *pipeline.Retry, attempt int) time.Duration {
	switch r.Backoff {
	case pipeline.BackoffLinear:
		return checkedMul(r.Delay, int64(attempt+1))
	case pipeline.BackoffExponential:
		if attempt >= 63 {
			return _maxDelay
		}
		return checkedMul(r.Delay, int64(1)<<uint(attempt)) //nolint:gosec // G115: attempt is non-negative (range loop) and bounds-checked above
	default:
		return r.Delay
	}
}

// checkedMul multiplies a duration by a scalar, capping at _maxDelay on overflow.
// Returns d when d <= 0 or n == 1, and 0 when n <= 0.
func checkedMul(d time.Duration, n int64) time.Duration {
	if n <= 0 {
		return 0
	}
	if d <= 0 || n == 1 {
		return d
	}
	if int64(d) > math.MaxInt64/n {
		return _maxDelay
	}
	return d * time.Duration(n)
}

// bridgeStatus returns a closure that forwards JobStatusMsg events from
// displayCh to sender. JobDoneMsg is sent on all exit paths (normal close
// and context cancellation). On context cancellation the bridge drains
// displayCh so the upstream Solve can exit.
// The caller is responsible for running the returned func (e.g. via wg.Go).
func bridgeStatus(ctx context.Context, sender progress.Sender, name string, displayCh <-chan *client.SolveStatus) func() {
	return func() {
		defer func() { sender.Send(progress.JobDoneMsg{Job: name}) }()
		for {
			select {
			case <-ctx.Done():
				//revive:disable-next-line:empty-block // drain so sender can close ch
				for range displayCh {
				}
				return
			case status, ok := <-displayCh:
				if !ok {
					return
				}
				sender.Send(progress.JobStatusMsg{Job: name, Status: status})
			}
		}
	}
}

// solveJobInput groups parameters for solveJob (CS-05).
type solveJobInput struct {
	name         string
	def          *llb.Definition
	cfg          runConfig
	stepTimeouts map[string]time.Duration
	cmdInfos     map[string]progress.CmdInfo
}

func solveJob(ctx context.Context, in solveJobInput) error {
	if in.def == nil {
		return ErrNilDefinition
	}

	ch := make(chan *client.SolveStatus)
	displayCh := teeStatus(ctx, ch, in.cfg.collector, in.cfg.secretValues, in.name)

	in.cfg.sender.Send(progress.JobAddedMsg{Job: in.name, StepTimeouts: in.stepTimeouts, CmdInfos: in.cmdInfos})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, in.cfg.sender, in.name, displayCh))

	_, err := in.cfg.solver.Solve(ctx, in.def, client.SolveOpt{
		LocalMounts:  in.cfg.localMounts,
		CacheExports: in.cfg.cacheExports,
		CacheImports: in.cfg.cacheImports,
		Session:      in.cfg.session,
	}, ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving job: %w", err)
	}
	return nil
}

// cleanTimeoutError detects step-level timeouts by extracting the
// *gatewaypb.ExitError from the error chain (via errors.As) and checking
// for exit codes 124 (GNU coreutils timeout convention) and 137 (BusyBox
// timeout -s KILL / 128+9). Note that 137 is ambiguous because OOM kills
// also produce 128+9, but we treat it as a timeout here since the caller
// gates this behind step-level timeout configuration.
// Returns a *StepTimeoutError with the original ExitError chained.
func cleanTimeoutError(err error, jobName string, stepTimeouts map[string]time.Duration) error {
	var exitErr *gatewaypb.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}
	// 124: GNU coreutils timeout; 137: BusyBox timeout -s KILL (128+9).
	if exitErr.ExitCode != 124 && exitErr.ExitCode != 137 {
		return err
	}
	// Extract step name only when the mapping is unambiguous (single entry).
	// With multiple step timeouts we cannot determine which step timed out
	// from the exit code alone, so we leave StepName/Timeout empty.
	var stepName string
	var timeout time.Duration
	if len(stepTimeouts) == 1 {
		for vertexName, t := range stepTimeouts {
			parts := strings.SplitN(vertexName, "/", 3)
			if len(parts) == 3 {
				stepName = parts[1]
				timeout = t
			}
		}
	}
	return fmt.Errorf("%w: %w", &StepTimeoutError{
		JobName:  jobName,
		StepName: stepName,
		Timeout:  timeout,
	}, exitErr)
}

// solveExport solves an export LLB definition using the local exporter to
// write files to the host filesystem.
func solveExport(ctx context.Context, exp Export, cfg runConfig) error {
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
	displayName := "export:" + exp.JobName
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progress.JobAddedMsg{Job: displayName})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	_, err := cfg.solver.Solve(ctx, exp.Definition, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:      client.ExporterLocal,
			OutputDir: outputDir,
		}},
		CacheExports: cfg.cacheExports,
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving export: %w", err)
	}
	return nil
}

// runPublishes groups image publishes and solves them concurrently. Single-variant
// groups use the simple image exporter; multi-variant groups use the gateway Build API.
//
//revive:disable-next-line:cognitive-complexity runPublishes is a flat dispatch over group size and export flags; splitting it hurts readability.
func runPublishes(ctx context.Context, in RunInput, cfg runConfig) error {
	if len(in.ImagePublishes) == 0 {
		return nil
	}

	groups, err := groupPublishes(in.ImagePublishes)
	if err != nil {
		return err
	}
	for _, grp := range groups {
		if len(grp.Variants) > 1 && grp.ExportDocker {
			return fmt.Errorf("image %q: %w", grp.Image, ErrExportDockerMultiPlatform)
		}
	}

	pg, pctx := errgroup.WithContext(ctx)
	for _, grp := range groups {
		if len(grp.Variants) > 1 {
			pg.Go(func() error {
				if err := solveMultiPlatformPublish(pctx, grp, cfg); err != nil {
					return fmt.Errorf("publishing multi-platform %q: %w", grp.Image, err)
				}
				return nil
			})
			continue
		}
		pub := grp.Variants[0]
		switch {
		case pub.Push && pub.ExportDocker:
			pg.Go(func() error {
				eg, ectx := errgroup.WithContext(pctx)
				eg.Go(func() error {
					if err := solveImagePublish(ectx, pub, cfg); err != nil {
						return fmt.Errorf("publishing %q from job %q: %w", grp.Image, pub.JobName, err)
					}
					return nil
				})
				eg.Go(func() error {
					if err := solveImageExportDocker(ectx, pub, cfg); err != nil {
						return fmt.Errorf("export-docker %q from job %q: %w", grp.Image, pub.JobName, err)
					}
					return nil
				})
				return eg.Wait()
			})
		case pub.ExportDocker:
			pg.Go(func() error {
				if err := solveImageExportDocker(pctx, pub, cfg); err != nil {
					return fmt.Errorf("export-docker %q from job %q: %w", grp.Image, pub.JobName, err)
				}
				return nil
			})
		default:
			pg.Go(func() error {
				if err := solveImagePublish(pctx, pub, cfg); err != nil {
					return fmt.Errorf("publishing %q from job %q: %w", grp.Image, pub.JobName, err)
				}
				return nil
			})
		}
	}
	return pg.Wait()
}

// publishGroup groups image publish variants targeting the same image reference.
type publishGroup struct {
	Image        string
	Push         bool
	Insecure     bool
	ExportDocker bool
	Variants     []ImagePublish
}

// groupPublishes groups image publishes by image reference. All variants
// targeting the same image must agree on Push, Insecure, and ExportDocker;
// a mismatch returns ErrPublishSettingConflict. Single-variant groups use
// the simple image exporter; multi-variant groups use the gateway Build API.
func groupPublishes(pubs []ImagePublish) ([]publishGroup, error) {
	idx := make(map[string]int, len(pubs))
	var groups []publishGroup
	for _, pub := range pubs {
		if i, ok := idx[pub.Image]; ok {
			grp := &groups[i]
			if pub.Push != grp.Push || pub.Insecure != grp.Insecure || pub.ExportDocker != grp.ExportDocker {
				return nil, fmt.Errorf("image %q: %w", pub.Image, ErrPublishSettingConflict)
			}
			grp.Variants = append(grp.Variants, pub)
			continue
		}
		idx[pub.Image] = len(groups)
		groups = append(groups, publishGroup{
			Image:        pub.Image,
			Push:         pub.Push,
			Insecure:     pub.Insecure,
			ExportDocker: pub.ExportDocker,
			Variants:     []ImagePublish{pub},
		})
	}
	return groups, nil
}

// validateVariants checks that all variants have non-nil definitions and unique
// platforms. Returns ErrNilDefinition or ErrDuplicatePlatform on failure.
func validateVariants(variants []ImagePublish) error {
	seen := make(map[string]string, len(variants))
	for _, v := range variants {
		if v.Definition == nil {
			return fmt.Errorf("variant %q (%s): %w", v.JobName, v.Platform, ErrNilDefinition)
		}
		plat, err := platforms.Parse(v.Platform)
		if err != nil {
			return fmt.Errorf("parsing platform %q for job %q: %w", v.Platform, v.JobName, err)
		}
		pid := platforms.Format(plat)
		if prev, ok := seen[pid]; ok {
			return fmt.Errorf("platform %q: jobs %q and %q: %w", pid, prev, v.JobName, ErrDuplicatePlatform)
		}
		seen[pid] = v.JobName
	}
	return nil
}

// multiPlatformBuildFunc returns a gateway.BuildFunc that solves each variant's
// definition and assembles them into a multi-platform result with platform metadata.
func multiPlatformBuildFunc(variants []ImagePublish) gateway.BuildFunc {
	return func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		if err := validateVariants(variants); err != nil {
			return nil, err
		}

		res := gateway.NewResult()
		platList := make([]exptypes.Platform, 0, len(variants))
		for _, v := range variants {
			plat, err := platforms.Parse(v.Platform)
			if err != nil {
				return nil, fmt.Errorf("parsing platform %q for job %q: %w", v.Platform, v.JobName, err)
			}

			solveRes, err := c.Solve(ctx, gateway.SolveRequest{
				Definition: v.Definition.ToPB(),
			})
			if err != nil {
				return nil, fmt.Errorf("solving variant %q (%s): %w", v.JobName, v.Platform, err)
			}

			ref, err := solveRes.SingleRef()
			if err != nil {
				return nil, fmt.Errorf("getting ref for %q (%s): %w", v.JobName, v.Platform, err)
			}

			platformID := platforms.Format(plat)
			res.AddRef(platformID, ref)
			platList = append(platList, exptypes.Platform{
				ID:       platformID,
				Platform: plat,
			})
		}

		dt, err := json.Marshal(exptypes.Platforms{Platforms: platList})
		if err != nil {
			return nil, fmt.Errorf("marshaling platform metadata: %w", err)
		}
		res.AddMeta(exptypes.ExporterPlatformsKey, dt)

		return res, nil
	}
}

// solveMultiPlatformPublish assembles multiple platform variants into a manifest
// list using the gateway Build API.
func solveMultiPlatformPublish(ctx context.Context, grp publishGroup, cfg runConfig) error {
	attrs := map[string]string{"name": grp.Image}
	if grp.Push {
		attrs["push"] = "true"
	}
	if grp.Insecure {
		attrs["registry.insecure"] = "true"
	}

	ch := make(chan *client.SolveStatus)
	displayName := "publish:" + grp.Image
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progress.JobAddedMsg{Job: displayName})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	_, err := cfg.solver.Build(ctx, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:  client.ExporterImage,
			Attrs: attrs,
		}},
		CacheExports: cfg.cacheExports,
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, "", multiPlatformBuildFunc(grp.Variants), ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving multi-platform publish: %w", err)
	}
	return nil
}

// solveImagePublish solves a single-platform image publish using the image exporter.
func solveImagePublish(ctx context.Context, pub ImagePublish, cfg runConfig) error {
	if pub.Definition == nil {
		return ErrNilDefinition
	}

	attrs := map[string]string{"name": pub.Image}
	if pub.Push {
		attrs["push"] = "true"
	}
	if pub.Insecure {
		attrs["registry.insecure"] = "true"
	}

	ch := make(chan *client.SolveStatus)
	displayName := "publish:" + pub.JobName
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progress.JobAddedMsg{Job: displayName})
	var wg sync.WaitGroup
	wg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	_, err := cfg.solver.Solve(ctx, pub.Definition, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:  client.ExporterImage,
			Attrs: attrs,
		}},
		CacheExports: cfg.cacheExports,
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, ch)

	wg.Wait()

	if err != nil {
		return fmt.Errorf("solving publish: %w", err)
	}
	return nil
}

// solveImageExportDocker solves an image definition using the Docker exporter,
// piping the tarball directly into `docker load` via an io.Pipe.
func solveImageExportDocker(ctx context.Context, pub ImagePublish, cfg runConfig) error {
	if pub.Definition == nil {
		return ErrNilDefinition
	}

	pr, pw := io.Pipe()

	ch := make(chan *client.SolveStatus)
	displayName := "export-docker:" + pub.JobName
	displayCh := teeStatus(ctx, ch, cfg.collector, cfg.secretValues, displayName)

	cfg.sender.Send(progress.JobAddedMsg{Job: displayName})
	var bridgeWg sync.WaitGroup
	bridgeWg.Go(bridgeStatus(ctx, cfg.sender, displayName, displayCh))

	eg, ectx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		defer func() { _ = pr.Close() }()
		if err := cfg.rt.LoadImage(ectx, pr); err != nil {
			return fmt.Errorf("loading image: %w", err)
		}
		return nil
	})

	eg.Go(func() error {
		defer func() { _ = pw.Close() }()
		_, err := cfg.solver.Solve(ectx, pub.Definition, client.SolveOpt{
			Exports: []client.ExportEntry{{
				Type:  client.ExporterDocker,
				Attrs: map[string]string{"name": pub.Image},
				Output: func(_ map[string]string) (io.WriteCloser, error) {
					return pw, nil
				},
			}},
			CacheExports: cfg.cacheExports,
			CacheImports: cfg.cacheImports,
			Session:      cfg.session,
		}, ch)
		if err != nil {
			return fmt.Errorf("solving export-docker: %w", err)
		}
		return nil
	})

	err := eg.Wait()
	bridgeWg.Wait()
	return err
}

// collectDepOutputs gathers parsed outputs from completed dependency nodes.
func collectDepOutputs(nodes map[string]*dagNode, deps []string) map[string]map[string]string {
	result := make(map[string]map[string]string, len(deps))
	for _, dep := range deps {
		if n, ok := nodes[dep]; ok && n.outputs != nil {
			result[dep] = n.outputs
		}
	}
	return result
}

// extractOutputs solves an output extraction definition to a temp directory,
// reads the output file, parses KEY=VALUE lines, and returns the result.
func extractOutputs(ctx context.Context, def *llb.Definition, cfg runConfig) (map[string]string, error) {
	dir, err := os.MkdirTemp("", "cicada-output-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ch := make(chan *client.SolveStatus)
	go drainChannel(ch)

	_, err = cfg.solver.Solve(ctx, def, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:      client.ExporterLocal,
			OutputDir: dir,
		}},
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, ch)
	if err != nil {
		return nil, fmt.Errorf("solving output extraction: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "output"))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading output file: %w", err)
	}

	return parseOutputLines(string(data)), nil
}

// parseOutputLines parses KEY=VALUE lines from a $CICADA_OUTPUT file.
// Empty lines and lines without '=' are skipped.
func parseOutputLines(content string) map[string]string {
	result := make(map[string]string)
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		result[key] = value
	}
	return result
}

// drainChannel discards remaining items from ch so the sender is not blocked.
func drainChannel(ch <-chan *client.SolveStatus) {
	//revive:disable-next-line:empty-block // intentionally discarding remaining events
	for range ch {
	}
}

// teeStatus interposes a Collector and secret redaction between the source
// status channel and the display consumer. If both collector and secrets are
// nil/empty, returns src directly (zero overhead). On context cancellation
// the goroutine drains src so the Solve sender can exit.
//
//revive:disable-next-line:cognitive-complexity teeStatus is a channel-forwarding goroutine; splitting it hurts readability.
func teeStatus(ctx context.Context, src <-chan *client.SolveStatus, collector *cache.Collector, secrets map[string]string, jobName string) <-chan *client.SolveStatus {
	if collector == nil && len(secrets) == 0 {
		return src
	}
	out := make(chan *client.SolveStatus)
	go func() {
		defer close(out)
		defer drainChannel(src)
		for {
			select {
			case status, ok := <-src:
				if !ok {
					return
				}
				redactStatus(status, secrets)
				if collector != nil {
					collector.Observe(jobName, status)
				}
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
