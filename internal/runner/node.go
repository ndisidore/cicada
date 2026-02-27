package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	gatewaypb "github.com/moby/buildkit/frontend/gateway/pb"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	rm "github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

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
				cfg.sender.Send(progressmodel.JobSkippedMsg{Job: node.job.Name})
				return nil
			}
		case <-ctx.Done():
			node.err = fmt.Errorf("job %q waiting for deps: %w", node.job.Name, ctx.Err())
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
			node.err = fmt.Errorf("job %q when: %w", node.job.Name, err)
			return node.err
		}
		if !result {
			node.skipped = true
			cfg.sender.Send(progressmodel.JobSkippedMsg{Job: node.job.Name})
			return nil
		}
	}

	if err := cfg.sem.Acquire(ctx, 1); err != nil {
		node.err = fmt.Errorf("job %q acquire semaphore: %w", node.job.Name, err)
		return node.err
	}
	defer cfg.sem.Release(1)

	tracer := cfg.tracer
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider().Tracer("cicada")
	}
	jobCtx, jobSpan := tracer.Start(ctx, "job/"+node.job.Name,
		trace.WithAttributes(attribute.String("job.name", node.job.Name)),
	)
	defer jobSpan.End()

	// Apply job-level timeout.
	solveCtx := jobCtx
	if node.job.Timeout > 0 {
		var cancel context.CancelFunc
		solveCtx, cancel = context.WithTimeoutCause(jobCtx, node.job.Timeout,
			&rm.JobTimeoutError{JobName: node.job.Name, Timeout: node.job.Timeout})
		defer cancel()
	}

	err := retryJob(solveCtx, node, cfg)
	if err != nil {
		if cause := context.Cause(solveCtx); errors.Is(cause, rm.ErrJobTimeout) {
			cfg.sender.Send(progressmodel.JobTimeoutMsg{Job: node.job.Name, Timeout: node.job.Timeout})
			node.err = progressmodel.NewDisplayError(
				fmt.Sprintf("job %q timed out after %s", node.job.Name, node.job.Timeout),
				cause,
			)
			jobSpan.RecordError(node.err)
			jobSpan.SetStatus(codes.Error, node.err.Error())
			return node.err
		}
		if len(node.job.StepTimeouts) > 0 {
			err = cleanTimeoutError(err, node.job.Name, node.job.StepTimeouts)
		}
		var exitErr *gatewaypb.ExitError
		if errors.As(err, &exitErr) {
			node.err = progressmodel.NewDisplayError(
				fmt.Sprintf("job %q: exit code: %d", node.job.Name, exitErr.ExitCode),
				err,
			)
			jobSpan.RecordError(node.err)
			jobSpan.SetStatus(codes.Error, node.err.Error())
			return node.err
		}
		node.err = fmt.Errorf("job %q: %w", node.job.Name, err)
		jobSpan.RecordError(node.err)
		jobSpan.SetStatus(codes.Error, node.err.Error())
		return node.err
	}

	for _, step := range node.job.SkippedSteps {
		cfg.sender.Send(progressmodel.StepSkippedMsg{Job: node.job.Name, Step: step})
	}

	// Extract outputs for downstream deferred conditions.
	// Step-controlled jobs extract outputs inside the gateway BuildFunc.
	if node.outputs == nil && node.job.OutputDef != nil {
		node.outputs, err = extractOutputs(ctx, node.job.OutputDef, cfg)
		if err != nil {
			node.err = fmt.Errorf("job %q output extraction: %w", node.job.Name, err)
			return node.err
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

		cfg.sender.Send(progressmodel.JobRetryMsg{
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

// _maxDelay caps retry delays to prevent overflow.
const _maxDelay = time.Duration(math.MaxInt64)

// retryDelay computes the delay for a given retry attempt based on the backoff strategy.
// Results are capped at math.MaxInt64 nanoseconds to prevent overflow.
func retryDelay(r *pipelinemodel.Retry, attempt int) time.Duration {
	switch r.Backoff {
	case pipelinemodel.BackoffLinear:
		return checkedMul(r.Delay, int64(attempt+1))
	case pipelinemodel.BackoffExponential:
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
