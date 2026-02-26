package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	rm "github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/internal/tracing"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// solveJobInput groups parameters for solveJob (CS-05).
type solveJobInput struct {
	name         string
	def          *llb.Definition
	cfg          runConfig
	stepTimeouts map[string]time.Duration
	cmdInfos     map[string]progressmodel.CmdInfo
}

func solveJob(ctx context.Context, in solveJobInput) error {
	if in.def == nil {
		return fmt.Errorf("job %q: %w", in.name, rm.ErrNilDefinition)
	}

	ch := make(chan *client.SolveStatus)
	var obs rm.StatusObserver
	if in.cfg.tracer != nil {
		obs = tracing.NewObserver(ctx, in.cfg.tracer)
	}
	displayCh := teeStatus(ctx, teeStatusInput{
		src: ch, collector: in.cfg.collector,
		observer: obs, secrets: in.cfg.secretValues, jobName: in.name,
	})

	in.cfg.sender.Send(progressmodel.JobAddedMsg{Job: in.name, StepTimeouts: in.stepTimeouts, CmdInfos: in.cmdInfos})
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

// solveJobSteps uses the gateway API to execute a job's steps individually,
// enabling per-step retry and allow-failure.
func solveJobSteps(ctx context.Context, node *dagNode, cfg runConfig) error {
	// Merge all step timeouts and cmd infos into single maps for JobAddedMsg.
	var merged map[string]time.Duration
	var mergedCmds map[string]progressmodel.CmdInfo
	for _, step := range node.job.Steps {
		if step.Timeouts != nil {
			if merged == nil {
				merged = make(map[string]time.Duration)
			}
			maps.Copy(merged, step.Timeouts)
		}
		if step.CmdInfos != nil {
			if mergedCmds == nil {
				mergedCmds = make(map[string]progressmodel.CmdInfo)
			}
			maps.Copy(mergedCmds, step.CmdInfos)
		}
	}

	ch := make(chan *client.SolveStatus)
	var obs rm.StatusObserver
	if cfg.tracer != nil {
		obs = tracing.NewObserver(ctx, cfg.tracer)
	}
	displayCh := teeStatus(ctx, teeStatusInput{
		src: ch, collector: cfg.collector,
		observer: obs, secrets: cfg.secretValues, jobName: node.job.Name,
	})

	cfg.sender.Send(progressmodel.JobAddedMsg{Job: node.job.Name, StepTimeouts: merged, CmdInfos: mergedCmds})
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
					cfg.sender.Send(progressmodel.StepAllowedFailureMsg{
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
	step    rm.StepExec
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

		in.cfg.sender.Send(progressmodel.StepRetryMsg{
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
	return fmt.Errorf("%w: %w", &rm.StepTimeoutError{
		JobName:  jobName,
		StepName: stepName,
		Timeout:  timeout,
	}, exitErr)
}
