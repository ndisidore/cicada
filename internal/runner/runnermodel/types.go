// Package runnermodel provides pure data types and interfaces for the runner
// package. Business logic (DAG building, solving, retry, export, publish)
// lives in the parent runner package.
package runnermodel

import (
	"context"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/tonistiigi/fsutil"
	"go.opentelemetry.io/otel/trace"

	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

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

// StatusObserver receives BuildKit SolveStatus updates for a single job.
// Observe is called for each status update; Flush is called on solve
// completion (normal or cancelled) so implementations can close open spans.
type StatusObserver interface {
	Observe(status *client.SolveStatus)
	Flush()
}

// noopObserver is a zero-cost StatusObserver used when tracing is disabled.
type noopObserver struct{}

func (noopObserver) Observe(*client.SolveStatus) {}
func (noopObserver) Flush()                      {}

// NoopObserver returns a StatusObserver that discards all events.
func NoopObserver() StatusObserver { return noopObserver{} }

// Job pairs an LLB definition with its human-readable job name and dependencies.
type Job struct {
	Name         string
	Definition   *llb.Definition
	DependsOn    []string
	When         DeferredEvaluator                // deferred condition; nil = always run
	Env          map[string]string                // pipeline-scoped env for deferred condition evaluation
	Matrix       map[string]string                // matrix dimension values for deferred condition evaluation
	OutputDef    *llb.Definition                  // extracts /cicada/output for deferred conditions
	SkippedSteps []string                         // step names skipped by static when conditions
	Timeout      time.Duration                    // job-level timeout; 0 = no timeout
	Retry        *pipelinemodel.Retry             // job-level retry config; nil = no retry
	StepTimeouts map[string]time.Duration         // vertex name -> configured timeout; nil = no step timeouts
	CmdInfos     map[string]progressmodel.CmdInfo // vertex name -> command metadata
	Steps        []StepExec                       // per-step execution; nil = monolithic solve
	BaseState    llb.State                        // pre-step LLB state; set when Steps is non-nil
}

// StepExec describes a single step for gateway-based step-controlled execution.
type StepExec struct {
	Name         string
	Retry        *pipelinemodel.Retry
	AllowFailure bool
	Timeouts     map[string]time.Duration
	CmdInfos     map[string]progressmodel.CmdInfo // vertex name -> command metadata
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
	// PipelineName is used as an attribute on the root trace span.
	PipelineName string
	// Solver is the BuildKit API client used to solve LLB definitions.
	Solver Solver
	// Runtime is the container runtime used for image loading (export-docker).
	Runtime runtime.Runtime
	// Jobs contains the LLB definitions and job names to execute.
	Jobs []Job
	// LocalMounts maps mount names to local filesystem sources.
	LocalMounts map[string]fsutil.FS
	// Sender delivers progress messages to the display.
	Sender progressmodel.Sender
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
	// Tracer instruments pipeline.run and job spans. Nil or noop tracer disables tracing.
	Tracer trace.Tracer
}
