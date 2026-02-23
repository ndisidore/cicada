package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/ndisidore/cicada/pkg/conditional"
	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// EvaluateConditions evaluates static when conditions on a pipeline, removing
// jobs and steps whose conditions evaluate to false. Deferred conditions
// (those referencing output()) are kept for runtime evaluation. Returns a
// ConditionResult with the filtered pipeline, names of skipped jobs, and a
// map of skipped steps per job.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic,function-length EvaluateConditions is a linear pipeline of filter operations; splitting it hurts readability.
func EvaluateConditions(ctx context.Context, p pm.Pipeline, wctx conditional.Context) (pm.ConditionResult, error) {
	result := p.Clone()
	var skippedNames []string
	logger := slogctx.FromContext(ctx)

	// Phase 1: Evaluate job-level non-deferred conditions.
	var kept []pm.Job
	skipped := make(map[string]struct{})
	for i := range result.Jobs {
		job := &result.Jobs[i]
		if job.When != nil && !job.When.Deferred {
			jobCtx := wctx
			jobCtx.PipelineEnv = BuildEnvScope(p.Env, job.Env)
			jobCtx.Matrix = job.MatrixValues
			ok, err := job.When.Evaluate(jobCtx)
			if err != nil {
				return pm.ConditionResult{}, fmt.Errorf("job %q: %w: %w", job.Name, pm.ErrJobCondition, err)
			}
			if !ok {
				skipped[job.Name] = struct{}{}
				skippedNames = append(skippedNames, job.Name)
				continue
			}
		}
		kept = append(kept, *job)
	}
	result.Jobs = kept

	// Clean up DependsOn and Artifact references to skipped jobs.
	for i := range result.Jobs {
		cleanJobRefs(logger, &result.Jobs[i], skipped)
	}

	// Phase 2: Evaluate step-level conditions within remaining jobs.
	skippedSteps := make(map[string][]string)
	var emptyJobs []string
	for i := range result.Jobs {
		job := &result.Jobs[i]
		var keptSteps []pm.Step
		for si := range job.Steps {
			step := &job.Steps[si]
			if step.When != nil && !step.When.Deferred {
				stepCtx := wctx
				stepCtx.PipelineEnv = BuildEnvScope(p.Env, mergeEnv(job.Env, step.Env))
				stepCtx.Matrix = job.MatrixValues
				ok, err := step.When.Evaluate(stepCtx)
				if err != nil {
					return pm.ConditionResult{}, fmt.Errorf("job %q step %q: %w: %w", job.Name, step.Name, pm.ErrStepCondition, err)
				}
				if !ok {
					skippedSteps[job.Name] = append(skippedSteps[job.Name], step.Name)
					continue
				}
			}
			keptSteps = append(keptSteps, *step)
		}
		job.Steps = keptSteps
		if len(job.Steps) == 0 {
			emptyJobs = append(emptyJobs, job.Name)
		}
	}

	// Phase 3: Remove jobs whose steps were all skipped.
	// The job appears in both Skipped (whole-job skip) and SkippedSteps
	// (per-step detail) so callers retain visibility into which steps
	// caused the job to become empty.
	if len(emptyJobs) > 0 {
		emptySet := make(map[string]struct{}, len(emptyJobs))
		for _, name := range emptyJobs {
			emptySet[name] = struct{}{}
		}
		var finalJobs []pm.Job
		for i := range result.Jobs {
			if _, isEmpty := emptySet[result.Jobs[i].Name]; !isEmpty {
				finalJobs = append(finalJobs, result.Jobs[i])
			}
		}
		result.Jobs = finalJobs
		for i := range result.Jobs {
			cleanJobRefs(logger, &result.Jobs[i], emptySet)
		}
		skippedNames = append(skippedNames, emptyJobs...)
	}

	result.TopoOrder = nil
	return pm.ConditionResult{Pipeline: result, Skipped: skippedNames, SkippedSteps: skippedSteps}, nil
}

// cleanJobRefs removes references to skipped jobs from DependsOn and Artifacts.
func cleanJobRefs(logger *slog.Logger, job *pm.Job, skipped map[string]struct{}) {
	job.DependsOn = slices.DeleteFunc(job.DependsOn, func(dep string) bool {
		_, skip := skipped[dep]
		if skip {
			logger.Debug("pruning dependency on skipped job",
				slog.String("job", job.Name),
				slog.String("dep", dep),
			)
		}
		return skip
	})
	job.Artifacts = slices.DeleteFunc(job.Artifacts, func(a pm.Artifact) bool {
		_, skip := skipped[a.From]
		if skip {
			logger.Warn("dropping artifact from skipped job",
				slog.String("job", job.Name),
				slog.String("from", a.From),
			)
		}
		return skip
	})
	for si := range job.Steps {
		job.Steps[si].Artifacts = slices.DeleteFunc(job.Steps[si].Artifacts, func(a pm.Artifact) bool {
			_, skip := skipped[a.From]
			if skip {
				logger.Warn("dropping step artifact from skipped job",
					slog.String("job", job.Name),
					slog.String("step", job.Steps[si].Name),
					slog.String("from", a.From),
				)
			}
			return skip
		})
	}
}
