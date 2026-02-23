package pipeline

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// Validate checks that the pipeline is well-formed and returns job indices
// in topological order (dependencies first) on success. On success it also
// sets p.TopoOrder to the returned ordering.
// A single 3-state DFS handles unknown-dep, cycle, and ordering in one pass.
func Validate(p *pm.Pipeline) ([]int, error) {
	if len(p.Jobs) == 0 {
		return nil, pm.ErrEmptyPipeline
	}

	if err := validateEnvVars("pipeline", p.Env); err != nil {
		return nil, err
	}
	if err := validateSecretDecls("pipeline", p.Secrets); err != nil {
		return nil, err
	}

	declaredSecrets := secretDeclSet(p.Secrets)

	g := jobGraph{
		jobs:  p.Jobs,
		index: make(map[string]int, len(p.Jobs)),
	}

	for i := range p.Jobs {
		if err := validateJob(i, &p.Jobs[i], &g, declaredSecrets); err != nil {
			return nil, err
		}
	}

	if err := checkSelfDeps(p.Jobs); err != nil {
		return nil, err
	}

	order, err := g.topoSort()
	if err != nil {
		return nil, err
	}
	p.TopoOrder = order
	return order, nil
}

// validateJob checks a single job for name, image, steps, and dependency validity.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic validateJob is a linear sequence of field checks; splitting it hurts readability.
func validateJob(idx int, j *pm.Job, g *jobGraph, declaredSecrets map[string]struct{}) error {
	if j.Name == "" {
		return fmt.Errorf("job at index %d: %w", idx, pm.ErrEmptyJobName)
	}
	if !_validName.MatchString(j.Name) {
		return fmt.Errorf("job %q: %w (must match %s)", j.Name, pm.ErrInvalidName, _validName)
	}
	if _, exists := g.index[j.Name]; exists {
		return fmt.Errorf("job %q: %w", j.Name, pm.ErrDuplicateJob)
	}
	g.index[j.Name] = idx
	if j.Image == "" {
		return fmt.Errorf("job %q: %w", j.Name, pm.ErrMissingImage)
	}
	if len(j.Steps) == 0 {
		return fmt.Errorf("job %q: %w", j.Name, pm.ErrEmptyJob)
	}
	jobScope := fmt.Sprintf("job %q", j.Name)
	if err := validateEnvVars(jobScope, j.Env); err != nil {
		return err
	}
	if err := validateExports(jobScope, j.Exports); err != nil {
		return err
	}
	if err := validateArtifacts(jobScope, j.Artifacts, j.DependsOn); err != nil {
		return err
	}
	if err := validatePublish(jobScope, j.Publish); err != nil {
		return err
	}
	if err := validateTimeout(jobScope, j.Timeout); err != nil {
		return err
	}
	if err := validateRetry(jobScope, j.Retry); err != nil {
		return err
	}
	if err := validateShell(jobScope, j.Shell); err != nil {
		return err
	}
	jobEnvUsed, jobMountUsed, err := validateSecretRefs(jobScope, j.Secrets, declaredSecrets)
	if err != nil {
		return err
	}
	// Jobs allow both static and deferred (output()-referencing) conditions,
	// so only syntax/type validation is needed here.
	if j.When != nil {
		if err := j.When.Validate(); err != nil {
			return fmt.Errorf("%s: %w: %w", jobScope, pm.ErrInvalidCondition, err)
		}
	}

	// Validate steps within the job.
	stepNames := make(map[string]struct{}, len(j.Steps))
	for si := range j.Steps {
		s := &j.Steps[si]
		if err := validateStep(j.Name, si, s, stepNames); err != nil {
			return err
		}
		stepScope := fmt.Sprintf("job %q step %q", j.Name, s.Name)
		if err := validateArtifacts(stepScope, s.Artifacts, j.DependsOn); err != nil {
			return err
		}
		// Pass job-level env/mount maps so step refs that collide with job
		// targets are rejected.
		if _, _, err := validateSecretRefsWithUsed(stepScope, s.Secrets, declaredSecrets,
			maps.Clone(jobEnvUsed), maps.Clone(jobMountUsed)); err != nil {
			return err
		}
		if err := validateSecretInheritance(stepScope, j.Secrets, s.Secrets); err != nil {
			return err
		}
	}

	return nil
}

// validateStep checks a single step within a job.
func validateStep(jobName string, idx int, s *pm.Step, seen map[string]struct{}) error {
	if s.Name == "" {
		return fmt.Errorf("job %q: step at index %d: %w", jobName, idx, pm.ErrEmptyStepName)
	}
	if !_validName.MatchString(s.Name) {
		return fmt.Errorf("job %q: step %q: %w (must match %s)", jobName, s.Name, pm.ErrInvalidName, _validName)
	}
	if _, exists := seen[s.Name]; exists {
		return fmt.Errorf("job %q: step %q: %w", jobName, s.Name, pm.ErrDuplicateStep)
	}
	seen[s.Name] = struct{}{}
	if len(s.Run) == 0 {
		return fmt.Errorf("job %q: step %q: %w", jobName, s.Name, pm.ErrMissingRun)
	}
	for _, cmd := range s.Run {
		if strings.TrimSpace(cmd) == "" {
			return fmt.Errorf("job %q: step %q: %w", jobName, s.Name, pm.ErrEmptyRunCommand)
		}
	}
	// Steps cannot reference output() (deferred expressions), so validate
	// and reject them explicitly.
	if s.When != nil {
		if err := s.When.Validate(); err != nil {
			return fmt.Errorf("job %q step %q: %w: %w", jobName, s.Name, pm.ErrInvalidCondition, err)
		}
		if s.When.Deferred {
			return fmt.Errorf("job %q step %q: %w", jobName, s.Name, pm.ErrDeferredStepWhen)
		}
	}
	stepScope := fmt.Sprintf("job %q step %q", jobName, s.Name)
	if err := validateTimeout(stepScope, s.Timeout); err != nil {
		return err
	}
	if err := validateShell(stepScope, s.Shell); err != nil {
		return err
	}
	if err := validateEnvVars(stepScope, s.Env); err != nil {
		return err
	}
	if err := validateRetry(stepScope, s.Retry); err != nil {
		return err
	}
	return validateExports(stepScope, s.Exports)
}

// validateEnvVars checks env vars for empty keys and duplicates within a scope.
func validateEnvVars(scope string, envs []pm.EnvVar) error {
	seen := make(map[string]struct{}, len(envs))
	for _, e := range envs {
		if e.Key == "" {
			return fmt.Errorf("%s: %w", scope, pm.ErrEmptyEnvKey)
		}
		if _, ok := seen[e.Key]; ok {
			return fmt.Errorf("%s: env %q: %w", scope, e.Key, pm.ErrDuplicateEnvKey)
		}
		seen[e.Key] = struct{}{}
	}
	return nil
}

// validateExports checks export paths are non-empty, absolute, not root, and
// unique, and that Export.Local is present. Both file and directory paths
// (trailing /) are valid. Export.Local format is intentionally not validated --
// relative paths are resolved against the working directory at runtime by the
// runner.
func validateExports(scope string, exports []pm.Export) error {
	seen := make(map[string]struct{}, len(exports))
	for _, e := range exports {
		if e.Path == "" {
			return fmt.Errorf("%s: %w", scope, pm.ErrEmptyExportPath)
		}
		if !strings.HasPrefix(e.Path, "/") {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, pm.ErrRelativeExport)
		}
		if strings.TrimRight(e.Path, "/") == "" {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, pm.ErrRootExport)
		}
		if e.Local == "" {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, pm.ErrEmptyExportLocal)
		}
		if _, ok := seen[e.Path]; ok {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, pm.ErrDuplicateExport)
		}
		seen[e.Path] = struct{}{}
	}
	return nil
}

// validateArtifacts checks artifact fields and that From references a dependency.
//
//revive:disable-next-line:cognitive-complexity validateArtifacts is a linear sequence of field checks; splitting it hurts readability.
func validateArtifacts(scope string, artifacts []pm.Artifact, deps []string) error {
	depSet := make(map[string]struct{}, len(deps))
	for _, d := range deps {
		depSet[d] = struct{}{}
	}
	targetSeen := make(map[string]struct{}, len(artifacts))
	for _, a := range artifacts {
		if a.From == "" {
			return fmt.Errorf("%s: %w", scope, pm.ErrEmptyArtifactFrom)
		}
		if a.Source == "" {
			return fmt.Errorf("%s: artifact from %q: %w", scope, a.From, pm.ErrEmptyArtifactSource)
		}
		if !strings.HasPrefix(a.Source, "/") {
			return fmt.Errorf("%s: artifact source %q: %w", scope, a.Source, pm.ErrRelativeArtifactSource)
		}
		if a.Target == "" {
			return fmt.Errorf("%s: artifact from %q: %w", scope, a.From, pm.ErrEmptyArtifactTarget)
		}
		if _, ok := depSet[a.From]; !ok {
			return fmt.Errorf("%s: artifact from %q: %w", scope, a.From, pm.ErrArtifactNoDep)
		}
		if !strings.HasPrefix(a.Target, "/") {
			return fmt.Errorf("%s: artifact target %q: %w", scope, a.Target, pm.ErrRelativeArtifact)
		}
		if strings.TrimRight(a.Target, "/") == "" {
			return fmt.Errorf("%s: artifact target %q: %w", scope, a.Target, pm.ErrRootArtifact)
		}
		if _, ok := targetSeen[a.Target]; ok {
			return fmt.Errorf("%s: artifact target %q: %w", scope, a.Target, pm.ErrDuplicateArtifact)
		}
		targetSeen[a.Target] = struct{}{}
	}
	return nil
}

// validatePublish checks that a publish declaration has a non-empty image reference.
func validatePublish(scope string, pub *pm.Publish) error {
	if pub == nil {
		return nil
	}
	if strings.TrimSpace(pub.Image) == "" {
		return fmt.Errorf("%s: %w", scope, pm.ErrEmptyPublishImage)
	}
	return nil
}

// validateRetry checks that a retry config is valid.
func validateRetry(scope string, r *pm.Retry) error {
	if r == nil {
		return nil
	}
	if r.Attempts <= 0 {
		return fmt.Errorf("%s: %w", scope, pm.ErrInvalidRetryAttempts)
	}
	if r.Delay < 0 {
		return fmt.Errorf("%s: %w", scope, pm.ErrNegativeDelay)
	}
	switch r.Backoff {
	case "", pm.BackoffNone, pm.BackoffLinear, pm.BackoffExponential:
	default:
		return fmt.Errorf("%s: backoff %q: %w", scope, r.Backoff, pm.ErrInvalidBackoff)
	}
	return nil
}

// validateTimeout checks that a timeout duration is non-negative.
func validateTimeout(scope string, d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("%s: %w", scope, pm.ErrNegativeTimeout)
	}
	return nil
}

// validateShell checks that an explicitly-set shell has at least one argument.
// A nil shell (inherit) is valid; a non-nil empty shell is not.
func validateShell(scope string, s []string) error {
	if s != nil && len(s) == 0 {
		return fmt.Errorf("%s: %w", scope, pm.ErrEmptyShell)
	}
	return nil
}

// checkSelfDeps detects jobs that list themselves as a dependency.
func checkSelfDeps(jobs []pm.Job) error {
	for i := range jobs {
		if slices.Contains(jobs[i].DependsOn, jobs[i].Name) {
			return fmt.Errorf("job %q: %w", jobs[i].Name, pm.ErrSelfDependency)
		}
	}
	return nil
}

// jobGraph provides indexed graph operations over a job slice.
type jobGraph struct {
	jobs  []pm.Job
	index map[string]int
}

// resolveDep looks up a dependency by name, returning a clear error for unknown deps.
func (g *jobGraph) resolveDep(jobName, dep string) (int, error) {
	j, ok := g.index[dep]
	if !ok {
		return 0, fmt.Errorf(
			"job %q depends on %q: %w", jobName, dep, pm.ErrUnknownDep,
		)
	}
	return j, nil
}

func (g *jobGraph) topoSort() ([]int, error) {
	const (
		unvisited = iota
		visiting
		visited
	)

	state := make([]int, len(g.jobs))
	order := make([]int, 0, len(g.jobs))

	var visit func(int) error
	visit = func(i int) error {
		switch state[i] {
		case visited:
			return nil
		case visiting:
			return fmt.Errorf("job %q: %w", g.jobs[i].Name, pm.ErrCycleDetected)
		}
		state[i] = visiting
		for _, dep := range g.jobs[i].DependsOn {
			j, err := g.resolveDep(g.jobs[i].Name, dep)
			if err != nil {
				return err
			}
			if err := visit(j); err != nil {
				return err
			}
		}
		state[i] = visited
		order = append(order, i)
		return nil
	}

	for i := range g.jobs {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return order, nil
}
