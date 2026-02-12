package pipeline

import (
	"errors"
	"fmt"
	"slices"
)

// Sentinel errors for job filtering.
var (
	ErrUnknownStartAt       = errors.New("start-at job not found")
	ErrUnknownStopAfter     = errors.New("stop-after job not found")
	ErrUnknownStartAtStep   = errors.New("start-at step not found in job")
	ErrUnknownStopAfterStep = errors.New("stop-after step not found in job")
	ErrEmptyStepWindow      = errors.New("step window is empty (start-at step comes after stop-after step)")
	ErrEmptyFilterResult    = errors.New("filter produces no jobs")
	ErrOrphanedStep         = errors.New("step specified without job")
)

// FilterOpts configures partial pipeline execution.
type FilterOpts struct {
	StartAt   string // run from this job (or job:step) forward
	StopAfter string // run up to and including this job (or job:step)
}

// filterTarget holds a parsed filter flag value split into job and optional step.
type filterTarget struct {
	Job  string
	Step string // empty means job-level only
}

// parseFilterTarget splits "job" or "job:step" into components. A colon inside
// brackets (e.g. "build[platform=linux/amd64:latest]") is part of the job name,
// not a separator. Only job names contain bracket suffixes (matrix expansion);
// step names never have brackets.
func parseFilterTarget(s string) filterTarget {
	// Find the first colon that is not inside brackets ([...]).
	depth := 0
	for i := range len(s) {
		switch s[i] {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				return filterTarget{Job: s[:i], Step: s[i+1:]}
			}
		default:
		}
	}
	return filterTarget{Job: s}
}

// jobAdj holds forward (dependents) and backward (dependencies) adjacency
// maps pre-computed from a job slice.
type jobAdj struct {
	dependents   map[string][]string
	dependencies map[string][]string
}

// buildJobAdj builds adjacency maps and validates that StartAt / StopAfter
// refer to existing jobs (and steps, when specified).
func buildJobAdj(jobs []Job, startAt, stopAfter filterTarget) (jobAdj, error) {
	nameSet := make(map[string]struct{}, len(jobs))
	jobByName := make(map[string]*Job, len(jobs))
	adj := jobAdj{
		dependents:   make(map[string][]string, len(jobs)),
		dependencies: make(map[string][]string, len(jobs)),
	}
	for i := range jobs {
		name := jobs[i].Name
		nameSet[name] = struct{}{}
		jobByName[name] = &jobs[i]
		adj.dependencies[name] = jobs[i].DependsOn
		for _, dep := range jobs[i].DependsOn {
			adj.dependents[dep] = append(adj.dependents[dep], name)
		}
	}
	if err := validateTarget(startAt, nameSet, jobByName, ErrUnknownStartAt, ErrUnknownStartAtStep); err != nil {
		return jobAdj{}, err
	}
	if err := validateTarget(stopAfter, nameSet, jobByName, ErrUnknownStopAfter, ErrUnknownStopAfterStep); err != nil {
		return jobAdj{}, err
	}
	return adj, nil
}

// validateTarget checks that a filter target references an existing job and step.
func validateTarget(t filterTarget, nameSet map[string]struct{}, jobByName map[string]*Job, errJob, errStep error) error {
	if t.Job == "" {
		if t.Step != "" {
			return fmt.Errorf("step %q: %w", t.Step, ErrOrphanedStep)
		}
		return nil
	}
	if _, ok := nameSet[t.Job]; !ok {
		return fmt.Errorf("%q: %w", t.Job, errJob)
	}
	if t.Step != "" && !jobHasStep(jobByName[t.Job], t.Step) {
		return fmt.Errorf("job %q step %q: %w", t.Job, t.Step, errStep)
	}
	return nil
}

// jobHasStep reports whether j contains a step with the given name.
func jobHasStep(j *Job, step string) bool {
	for i := range j.Steps {
		if j.Steps[i].Name == step {
			return true
		}
	}
	return false
}

// stepIndex returns the index of the named step, or -1 if not found.
func stepIndex(steps []Step, name string) int {
	for i := range steps {
		if steps[i].Name == name {
			return i
		}
	}
	return -1
}

// FilterJobs selects a subgraph of jobs based on start-at / stop-after
// options. Jobs in the execution window retain their exports; transitive
// dependencies included only for build correctness have exports stripped.
// The original slice order is preserved.
//
// When a flag uses job:step syntax, step-level trimming is applied:
//   - stop-after job:step truncates steps after the named step.
//   - start-at job:step strips exports from steps before the named step.
//   - Both on the same job create a step window; error if window is empty.
func FilterJobs(jobs []Job, opts FilterOpts) ([]Job, error) {
	if opts.StartAt == "" && opts.StopAfter == "" {
		return jobs, nil
	}

	startAt := parseFilterTarget(opts.StartAt)
	stopAfter := parseFilterTarget(opts.StopAfter)

	adj, err := buildJobAdj(jobs, startAt, stopAfter)
	if err != nil {
		return nil, err
	}

	// Forward reachable from StartAt (the job itself + all downstream dependents).
	var forwardSet map[string]struct{}
	if startAt.Job != "" {
		forwardSet = bfsReachable(startAt.Job, adj.dependents)
	}

	// Backward reachable from StopAfter (the job itself + all upstream dependencies).
	var backwardSet map[string]struct{}
	if stopAfter.Job != "" {
		backwardSet = bfsReachable(stopAfter.Job, adj.dependencies)
	}

	executionWindow := intersectSets(forwardSet, backwardSet)
	if len(executionWindow) == 0 {
		return nil, fmt.Errorf(
			"start-at=%q stop-after=%q: %w",
			opts.StartAt, opts.StopAfter, ErrEmptyFilterResult,
		)
	}

	// Add transitive dependencies of execution window members.
	fullSet := make(map[string]struct{}, len(executionWindow))
	for name := range executionWindow {
		fullSet[name] = struct{}{}
	}
	for name := range executionWindow {
		addTransitiveDeps(name, adj.dependencies, fullSet)
	}

	// Filter jobs preserving original order; clone Steps to avoid aliasing
	// the input slice. Strip exports from dep-only jobs.
	filtered := make([]Job, 0, len(fullSet))
	for i := range jobs {
		if _, ok := fullSet[jobs[i].Name]; !ok {
			continue
		}
		j := jobs[i]
		j.Steps = slices.Clone(j.Steps)
		if _, inWindow := executionWindow[j.Name]; !inWindow {
			j.Exports = nil
			for si := range j.Steps {
				j.Steps[si].Exports = nil
			}
		}
		filtered = append(filtered, j)
	}

	// Apply step-level trimming for job:step targets.
	if err := applyStepTrimming(filtered, startAt, stopAfter); err != nil {
		return nil, err
	}

	return filtered, nil
}

// stepTrimOpts holds resolved step indices for a single job's trim operation.
// A negative index means no trimming for that direction.
type stepTrimOpts struct {
	StartIdx int // strip exports on steps before this index; -1 = no start trim
	StopIdx  int // truncate steps after this index; -1 = no stop trim
}

// applyStepTrimming modifies job steps in-place based on step-level filter targets.
// stop-after truncates steps after the named step; start-at strips exports from
// earlier steps. When both target the same job, error if start comes after stop.
// Callers must ensure jobs[i].Steps are pre-cloned (FilterJobs does this).
func applyStepTrimming(jobs []Job, startAt, stopAfter filterTarget) error {
	if startAt.Step == "" && stopAfter.Step == "" {
		return nil
	}

	for i := range jobs {
		trim := resolveStepTrim(jobs[i].Name, jobs[i].Steps, startAt, stopAfter)
		if trim.StartIdx < 0 && trim.StopIdx < 0 {
			continue
		}
		if trim.StartIdx >= 0 && trim.StopIdx >= 0 && trim.StartIdx > trim.StopIdx {
			return fmt.Errorf(
				"job %q: start-at step %q (index %d) comes after stop-after step %q (index %d): %w",
				jobs[i].Name, startAt.Step, trim.StartIdx, stopAfter.Step, trim.StopIdx, ErrEmptyStepWindow,
			)
		}
		if trim.StopIdx >= 0 {
			jobs[i].Steps = jobs[i].Steps[:trim.StopIdx+1]
		}
		if trim.StartIdx >= 0 {
			stripStepExports(jobs[i].Steps[:trim.StartIdx])
		}
	}
	return nil
}

// resolveStepTrim computes which step indices to trim for a given job.
func resolveStepTrim(jobName string, steps []Step, startAt, stopAfter filterTarget) stepTrimOpts {
	opts := stepTrimOpts{StartIdx: -1, StopIdx: -1}
	if startAt.Step != "" && jobName == startAt.Job {
		opts.StartIdx = stepIndex(steps, startAt.Step)
	}
	if stopAfter.Step != "" && jobName == stopAfter.Job {
		opts.StopIdx = stepIndex(steps, stopAfter.Step)
	}
	return opts
}

// stripStepExports nils out Exports on all given steps.
func stripStepExports(steps []Step) {
	for i := range steps {
		steps[i].Exports = nil
	}
}

// bfsReachable returns all nodes reachable from start (inclusive) via the
// given adjacency map.
func bfsReachable(start string, adj map[string][]string) map[string]struct{} {
	visited := map[string]struct{}{start: {}}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if _, ok := visited[next]; !ok {
				visited[next] = struct{}{}
				queue = append(queue, next)
			}
		}
	}
	return visited
}

// intersectSets returns the intersection of a and b. A nil set is treated
// as the universe (all elements pass).
func intersectSets(a, b map[string]struct{}) map[string]struct{} {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	// Iterate over the smaller set for efficiency.
	if len(a) > len(b) {
		a, b = b, a
	}
	result := make(map[string]struct{}, len(a))
	for k := range a {
		if _, ok := b[k]; ok {
			result[k] = struct{}{}
		}
	}
	return result
}

// addTransitiveDeps adds all transitive dependencies of name to visited via DFS.
func addTransitiveDeps(name string, deps map[string][]string, visited map[string]struct{}) {
	for _, dep := range deps[name] {
		if _, ok := visited[dep]; ok {
			continue
		}
		visited[dep] = struct{}{}
		addTransitiveDeps(dep, deps, visited)
	}
}
