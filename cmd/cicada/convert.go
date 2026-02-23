package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ndisidore/cicada/internal/builder"
	"github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/pkg/pipeline"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// buildNoCacheFilter converts CLI filter names into a set and warns about
// names that don't match any pipeline job.
func buildNoCacheFilter(ctx context.Context, filters []string, p pipelinemodel.Pipeline) map[string]struct{} {
	if len(filters) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(filters))
	for _, name := range filters {
		set[name] = struct{}{}
	}
	jobNames := make(map[string]struct{}, len(p.Jobs))
	for i := range p.Jobs {
		jobNames[p.Jobs[i].Name] = struct{}{}
	}
	for name := range set {
		if _, ok := jobNames[name]; !ok {
			slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelWarn, "no-cache-filter: no job matches", slog.String("name", name))
		}
	}
	return set
}

// buildDockerExportFilter converts CLI flag values into a set of job names
// whose published images should be loaded into the local Docker daemon.
// A value of "*" selects all jobs with a publish node.
func buildDockerExportFilter(ctx context.Context, flagValues []string, pubs []runnermodel.ImagePublish) map[string]struct{} {
	if len(flagValues) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(flagValues))
	for _, v := range flagValues {
		if v == "*" {
			all := make(map[string]struct{}, len(pubs))
			for i := range pubs {
				all[pubs[i].JobName] = struct{}{}
			}
			return all
		}
		set[v] = struct{}{}
	}
	pubJobs := make(map[string]struct{}, len(pubs))
	for i := range pubs {
		pubJobs[pubs[i].JobName] = struct{}{}
	}
	for name := range set {
		if _, ok := pubJobs[name]; !ok {
			slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelWarn,
				"with-docker-export: no publish job matches", slog.String("name", name))
		}
	}
	return set
}

// buildImagePublishes converts builder image exports into runner ImagePublish
// values, applying the --with-docker-export filter to set ExportDocker.
func buildImagePublishes(ctx context.Context, exports []builder.ImageExport, dockerExportFlags []string) []runnermodel.ImagePublish {
	pubs := make([]runnermodel.ImagePublish, len(exports))
	for i, ie := range exports {
		pubs[i] = runnermodel.ImagePublish{
			Definition: ie.Definition,
			JobName:    ie.JobName,
			Image:      ie.Publish.Image,
			Push:       ie.Publish.Push,
			Insecure:   ie.Publish.Insecure,
			Platform:   ie.Platform,
		}
	}
	filter := buildDockerExportFilter(ctx, dockerExportFlags, pubs)
	for i := range pubs {
		if _, ok := filter[pubs[i].JobName]; ok {
			pubs[i].ExportDocker = true
		}
	}
	return pubs
}

// pipelineJobInfo holds pre-indexed per-job metadata from the pipeline.
type pipelineJobInfo struct {
	DependsOn []string
	When      runnermodel.DeferredEvaluator // deferred condition; nil = no deferred condition
	Env       map[string]string             // pipeline-scoped env for deferred condition evaluation
	Matrix    map[string]string             // matrix dimension values for deferred condition evaluation
	Timeout   time.Duration                 // job-level timeout
	Retry     *pipelinemodel.Retry          // job-level retry config
}

// buildRunnerJobs converts a builder.Result into a slice of runnermodel.Job,
// carrying dependency information from the pipeline. Dependencies are resolved
// by name rather than positional index, so builder output ordering need not
// match pipeline declaration order.
func buildRunnerJobs(r builder.Result, p pipelinemodel.Pipeline, skippedSteps map[string][]string) ([]runnermodel.Job, error) {
	if len(r.Definitions) != len(r.JobNames) {
		return nil, fmt.Errorf("%w: %d definitions, %d job names",
			errResultMismatch, len(r.Definitions), len(r.JobNames))
	}

	// Index pipeline jobs by name for dependency + deferred when lookup.
	pipelineIdx := make(map[string]pipelineJobInfo, len(p.Jobs))
	for i := range p.Jobs {
		info := pipelineJobInfo{
			DependsOn: p.Jobs[i].DependsOn,
			Timeout:   p.Jobs[i].Timeout,
			Retry:     p.Jobs[i].Retry,
		}
		if p.Jobs[i].When != nil && p.Jobs[i].When.Deferred {
			info.When = p.Jobs[i].When
			info.Env = pipeline.BuildEnvScope(p.Env, p.Jobs[i].Env)
			info.Matrix = p.Jobs[i].MatrixValues
		}
		pipelineIdx[p.Jobs[i].Name] = info
	}

	jobs := make([]runnermodel.Job, len(r.Definitions))
	for i := range r.Definitions {
		info, ok := pipelineIdx[r.JobNames[i]]
		if !ok {
			return nil, fmt.Errorf("%w: %q", errUnknownBuilderJob, r.JobNames[i])
		}
		jobs[i] = runnermodel.Job{
			Name:         r.JobNames[i],
			Definition:   r.Definitions[i],
			DependsOn:    info.DependsOn,
			When:         info.When,
			Env:          info.Env,
			Matrix:       info.Matrix,
			OutputDef:    r.OutputDefs[r.JobNames[i]],
			SkippedSteps: skippedSteps[r.JobNames[i]],
			Timeout:      info.Timeout,
			Retry:        info.Retry,
			StepTimeouts: r.StepTimeouts[r.JobNames[i]],
			CmdInfos:     r.CmdInfos[r.JobNames[i]],
		}
		if defs, ok := r.StepDefs[r.JobNames[i]]; ok {
			jobs[i].Steps = convertStepDefs(defs)
			jobs[i].BaseState = r.BaseStates[r.JobNames[i]]
		}
	}
	return jobs, nil
}

// convertStepDefs converts builder StepDefs to runner StepExec values.
func convertStepDefs(defs []builder.StepDef) []runnermodel.StepExec {
	steps := make([]runnermodel.StepExec, len(defs))
	for i, d := range defs {
		steps[i] = runnermodel.StepExec{
			Name:         d.Name,
			Retry:        d.Retry,
			AllowFailure: d.AllowFailure,
			Timeouts:     d.Timeouts,
			CmdInfos:     d.CmdInfos,
			Build:        d.Build,
		}
	}
	return steps
}

func (a *app) printDryRun(name string, jobs []runnermodel.Job) {
	_, _ = fmt.Fprintf(a.stdout, "Pipeline '%s' is valid\n", name)
	_, _ = fmt.Fprintf(a.stdout, "  Jobs: %d\n", len(jobs))
	for _, j := range jobs {
		ops := 0
		if j.Definition != nil {
			ops = len(j.Definition.Def)
		}
		if len(j.DependsOn) > 0 {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (%d LLB ops, depends: %s)\n",
				j.Name, ops, strings.Join(j.DependsOn, ", "))
		} else {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (%d LLB ops)\n", j.Name, ops)
		}
	}
}

func (a *app) printPipelineSummary(name string, jobs []pipelinemodel.Job) {
	_, _ = fmt.Fprintf(a.stdout, "Pipeline '%s' is valid\n", name)
	_, _ = fmt.Fprintf(a.stdout, "  Jobs: %d\n", len(jobs))
	for _, j := range jobs {
		if len(j.DependsOn) > 0 {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (image: %s, depends: %s)\n",
				j.Name, j.Image, strings.Join(j.DependsOn, ", "))
		} else {
			_, _ = fmt.Fprintf(a.stdout, "    - %s (image: %s)\n", j.Name, j.Image)
		}
		for _, s := range j.Steps {
			_, _ = fmt.Fprintf(a.stdout, "      - %s\n", s.Name)
		}
	}
}
