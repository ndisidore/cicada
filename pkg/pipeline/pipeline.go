// Package pipeline defines the core types for a cicada CI/CD pipeline.
package pipeline

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ndisidore/cicada/pkg/conditional"
)

// Sentinel errors for pipeline validation.
var (
	ErrEmptyPipeline    = errors.New("pipeline has no jobs")
	ErrEmptyJobName     = errors.New("job has empty name")
	ErrDuplicateJob     = errors.New("duplicate job name")
	ErrEmptyStepName    = errors.New("step has empty name")
	ErrDuplicateStep    = errors.New("duplicate step name")
	ErrInvalidName      = errors.New("name contains invalid characters")
	ErrMissingImage     = errors.New("job missing image")
	ErrMissingRun       = errors.New("step has no run commands")
	ErrEmptyRunCommand  = errors.New("run command is empty or whitespace-only")
	ErrEmptyJob         = errors.New("job has no steps")
	ErrSelfDependency   = errors.New("job depends on itself")
	ErrUnknownDep       = errors.New("unknown dependency")
	ErrCycleDetected    = errors.New("dependency cycle detected")
	ErrEmptyMatrix      = errors.New("matrix has no dimensions")
	ErrEmptyDimension   = errors.New("dimension has no values")
	ErrInvalidDimName   = errors.New("invalid dimension name")
	ErrDuplicateDim     = errors.New("duplicate dimension name")
	ErrMatrixTooLarge   = errors.New("matrix produces too many combinations")
	ErrInvalidCondition = errors.New("invalid when condition")
	ErrDeferredStepWhen = errors.New("step when must not reference output()")
)

// Sentinel errors for modular configuration (includes, fragments, params).
var (
	ErrCircularInclude  = errors.New("circular include detected")
	ErrIncludeDepth     = errors.New("include depth limit exceeded")
	ErrMissingParam     = errors.New("missing required parameter")
	ErrUnknownParam     = errors.New("unknown parameter")
	ErrDuplicateParam   = errors.New("duplicate parameter name")
	ErrDuplicateAlias   = errors.New("duplicate include alias")
	ErrAliasCollision   = errors.New("alias collides with job name")
	ErrInvalidConflict  = errors.New("invalid on-conflict value")
	ErrPipelineNoParams = errors.New("pipeline does not accept parameters")
)

// Sentinel errors for retry, timeout, and shell configuration.
var (
	ErrInvalidRetryAttempts = errors.New("retry attempts must be positive")
	ErrInvalidBackoff       = errors.New("invalid backoff strategy")
	ErrNegativeDelay        = errors.New("retry delay must be non-negative")
	ErrNegativeTimeout      = errors.New("timeout must be non-negative")
	ErrEmptyShell           = errors.New("shell must have at least one argument")
)

// Sentinel errors for env vars, exports, artifacts, and publish.
var (
	ErrEmptyEnvKey            = errors.New("env key is empty")
	ErrEmptyExportPath        = errors.New("export path is empty")
	ErrDuplicateExport        = errors.New("duplicate export path")
	ErrArtifactNoDep          = errors.New("artifact references job not in depends-on")
	ErrDuplicateEnvKey        = errors.New("duplicate env key")
	ErrDuplicateArtifact      = errors.New("duplicate artifact target path")
	ErrRelativeExport         = errors.New("export path must be absolute")
	ErrRelativeArtifact       = errors.New("artifact target must be absolute")
	ErrRelativeArtifactSource = errors.New("artifact source must be absolute")
	ErrEmptyArtifactFrom      = errors.New("artifact from is empty")
	ErrEmptyArtifactSource    = errors.New("artifact source is empty")
	ErrEmptyArtifactTarget    = errors.New("artifact target is empty")
	ErrRootExport             = errors.New("export path must not be root")
	ErrRootArtifact           = errors.New("artifact target must not be root")
	ErrEmptyExportLocal       = errors.New("export local path is empty")
	ErrEmptyPublishImage      = errors.New("publish image reference is empty")
)

// BackoffStrategy for retry delay scaling.
type BackoffStrategy string

// BackoffStrategy values.
const (
	BackoffNone        BackoffStrategy = "none"
	BackoffLinear      BackoffStrategy = "linear"
	BackoffExponential BackoffStrategy = "exponential"
)

// Retry configures retry behavior for jobs and steps.
type Retry struct {
	Attempts int             // max number of retries (not counting first attempt)
	Delay    time.Duration   // base delay between retries
	Backoff  BackoffStrategy // "none" | "linear" | "exponential"
}

// ConflictStrategy determines behavior when job names collide during merge.
type ConflictStrategy int

// ConflictStrategy values.
const (
	ConflictError ConflictStrategy = iota
	ConflictSkip
)

// ParamDef defines a parameter accepted by a fragment.
type ParamDef struct {
	Name     string
	Default  string
	Required bool
}

// Fragment is a reusable collection of jobs with optional parameters.
type Fragment struct {
	Name   string
	Params []ParamDef
	Jobs   []Job
}

// _validName matches names: alphanumeric base with an optional bracket
// suffix for expanded matrix names (e.g. "build[platform=linux/amd64]").
// The bracket portion permits '/' and ':' for OCI platform specifiers; these
// names are used as map keys and display labels, not raw filesystem paths.
var _validName = regexp.MustCompile(`^[a-zA-Z0-9_-]+(\[[a-zA-Z0-9_.:/\-]+(=[a-zA-Z0-9_.:/\-]+)?(,[a-zA-Z0-9_.:/\-]+(=[a-zA-Z0-9_.:/\-]+)?)*\])?$`)

// _validDimName matches dimension names: alphanumeric, hyphens, underscores.
var _validDimName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Matrix defines a set of dimensions whose cartesian product generates
// concrete job variants during expansion.
type Matrix struct {
	Dimensions []Dimension
}

// Dimension is a single axis in a Matrix with one or more string values.
type Dimension struct {
	Name   string
	Values []string
}

// Combinations returns the cartesian product of all dimensions as a slice of
// maps. Each map maps dimension name to a single value. Returns an empty slice
// for an empty matrix and (nil, ErrMatrixTooLarge) on integer overflow.
func (m Matrix) Combinations() ([]map[string]string, error) {
	if len(m.Dimensions) == 0 {
		return []map[string]string{}, nil
	}
	// Count total combinations up front, guarding against overflow.
	total := 1
	for i := range m.Dimensions {
		n := len(m.Dimensions[i].Values)
		if n > 0 && total > math.MaxInt/n {
			return nil, ErrMatrixTooLarge
		}
		total *= n
	}
	combos := make([]map[string]string, 0, total)
	combos = append(combos, make(map[string]string, len(m.Dimensions)))
	for i := range m.Dimensions {
		dim := &m.Dimensions[i]
		next := make([]map[string]string, 0, len(combos)*len(dim.Values))
		for _, combo := range combos {
			for _, val := range dim.Values {
				cp := make(map[string]string, len(combo)+1)
				maps.Copy(cp, combo)
				cp[dim.Name] = val
				next = append(next, cp)
			}
		}
		combos = next
	}
	return combos, nil
}

// EnvVar represents a key-value environment variable.
type EnvVar struct {
	Key   string
	Value string
}

// Export declares a container path that a job produces as output.
type Export struct {
	Path  string // absolute container path to export
	Local string // host path for local export
}

// Artifact imports a file from a dependency job into this job's container.
type Artifact struct {
	From   string // dependency job name
	Source string // path inside dependency container
	Target string // absolute path inside this job's container
}

// Mount represents a bind mount from host to container.
type Mount struct {
	Source   string // host path or named volume to mount
	Target   string // absolute container path for the mount point
	ReadOnly bool   // if true, mount is read-only inside the container
}

// Cache represents a persistent cache volume.
type Cache struct {
	ID     string // unique identifier for this cache volume
	Target string // absolute container path where the cache is mounted
}

// Publish declares an OCI image export for a job's final filesystem.
type Publish struct {
	Image    string // OCI image reference (e.g. "ghcr.io/user/app:latest")
	Push     bool   // push to registry; parser defaults to true when omitted in KDL
	Insecure bool   // allow insecure registry
}

// Defaults holds pipeline-wide configuration inherited by all jobs.
// Image and Workdir fill empty job fields (set-if-absent). Mounts are
// prepended (defaults first, then job-specific). Env is merged with
// job values winning on key conflict.
type Defaults struct {
	Image   string   // default image for jobs that don't specify one
	Workdir string   // default workdir for jobs that don't specify one
	Mounts  []Mount  // bind mounts prepended to every job's mount list
	Env     []EnvVar // env vars merged into every job (job wins on conflict)
	Shell   []string // default shell args for all jobs/steps
}

// Pipeline represents a CI/CD pipeline parsed from KDL.
type Pipeline struct {
	Name      string
	Jobs      []Job
	Env       []EnvVar     // pipeline-level env vars inherited by all jobs
	Secrets   []SecretDecl // pipeline-level secret declarations
	Matrix    *Matrix      // pipeline-level matrix; nil if not set
	Defaults  *Defaults    // pipeline-wide defaults; nil if not set
	TopoOrder []int        // cached topological order from Validate; nil if not yet validated
}

// Job represents a grouping of sequential steps that share a container context.
// Jobs are the unit of parallelism, dependency, and matrix expansion.
type Job struct {
	Name         string
	Image        string
	Workdir      string
	Platform     string // OCI platform specifier (e.g. "linux/amd64"); empty means default
	DependsOn    []string
	Mounts       []Mount
	Caches       []Cache
	Env          []EnvVar    // job-level env vars (override pipeline-level)
	Exports      []Export    // container paths this job produces
	Artifacts    []Artifact  // files imported from dependency jobs
	Secrets      []SecretRef // secret refs injected into the job container
	Steps        []Step
	When         *conditional.When // conditional execution; nil = always run
	Matrix       *Matrix           // job-level matrix; nil if not set
	MatrixValues map[string]string // concrete dimension values for this expanded variant; nil for non-matrix jobs
	NoCache      bool              // disable cache for this job
	Publish      *Publish          // OCI image export; nil if not set
	Timeout      time.Duration     // job-level timeout; 0 = no timeout
	Retry        *Retry            // job-level retry config; nil = no retry
	Shell        []string          // custom shell args; nil = use default
}

// Step represents a single execution unit within a job.
// Steps within a job are sequential RUN layers on the same container state.
type Step struct {
	Name         string
	Run          []string
	Env          []EnvVar          // step-scoped env vars (additive to job)
	Workdir      string            // set workdir from this step onward (like Docker WORKDIR)
	Mounts       []Mount           // step-specific bind mounts (additive to job)
	Caches       []Cache           // step-specific cache volumes (additive to job)
	Secrets      []SecretRef       // step-scoped secret references (additive to job)
	Exports      []Export          // declares this step produces files for export
	Artifacts    []Artifact        // files imported from dependency jobs at this point
	When         *conditional.When // conditional execution; nil = always run
	NoCache      bool              // disable caching for this step
	Timeout      time.Duration     // step-level timeout; 0 = no timeout
	Shell        []string          // custom shell args; nil = inherit from job
	Retry        *Retry            // step-level retry config; nil = no retry
	AllowFailure bool              // if true, step failure does not fail the job
}

// Validate checks that the pipeline is well-formed and returns job indices
// in topological order (dependencies first) on success.
// A single 3-state DFS handles unknown-dep, cycle, and ordering in one pass.
func (p *Pipeline) Validate() ([]int, error) {
	if len(p.Jobs) == 0 {
		return nil, ErrEmptyPipeline
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
func validateJob(idx int, j *Job, g *jobGraph, declaredSecrets map[string]struct{}) error {
	if j.Name == "" {
		return fmt.Errorf("job at index %d: %w", idx, ErrEmptyJobName)
	}
	if !_validName.MatchString(j.Name) {
		return fmt.Errorf("job %q: %w (must match %s)", j.Name, ErrInvalidName, _validName)
	}
	if _, exists := g.index[j.Name]; exists {
		return fmt.Errorf("job %q: %w", j.Name, ErrDuplicateJob)
	}
	g.index[j.Name] = idx
	if j.Image == "" {
		return fmt.Errorf("job %q: %w", j.Name, ErrMissingImage)
	}
	if len(j.Steps) == 0 {
		return fmt.Errorf("job %q: %w", j.Name, ErrEmptyJob)
	}
	if err := validateEnvVars(fmt.Sprintf("job %q", j.Name), j.Env); err != nil {
		return err
	}
	jobScope := fmt.Sprintf("job %q", j.Name)
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
			return fmt.Errorf("%s: %w: %w", jobScope, ErrInvalidCondition, err)
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
func validateStep(jobName string, idx int, s *Step, seen map[string]struct{}) error {
	if s.Name == "" {
		return fmt.Errorf("job %q: step at index %d: %w", jobName, idx, ErrEmptyStepName)
	}
	if !_validName.MatchString(s.Name) {
		return fmt.Errorf("job %q: step %q: %w (must match %s)", jobName, s.Name, ErrInvalidName, _validName)
	}
	if _, exists := seen[s.Name]; exists {
		return fmt.Errorf("job %q: step %q: %w", jobName, s.Name, ErrDuplicateStep)
	}
	seen[s.Name] = struct{}{}
	if len(s.Run) == 0 {
		return fmt.Errorf("job %q: step %q: %w", jobName, s.Name, ErrMissingRun)
	}
	for _, cmd := range s.Run {
		if strings.TrimSpace(cmd) == "" {
			return fmt.Errorf("job %q: step %q: %w", jobName, s.Name, ErrEmptyRunCommand)
		}
	}
	// Steps cannot reference output() (deferred expressions), so validate
	// and reject them explicitly.
	if s.When != nil {
		if err := s.When.Validate(); err != nil {
			return fmt.Errorf("job %q step %q: %w: %w", jobName, s.Name, ErrInvalidCondition, err)
		}
		if s.When.Deferred {
			return fmt.Errorf("job %q step %q: %w", jobName, s.Name, ErrDeferredStepWhen)
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
func validateEnvVars(scope string, envs []EnvVar) error {
	seen := make(map[string]struct{}, len(envs))
	for _, e := range envs {
		if e.Key == "" {
			return fmt.Errorf("%s: %w", scope, ErrEmptyEnvKey)
		}
		if _, ok := seen[e.Key]; ok {
			return fmt.Errorf("%s: env %q: %w", scope, e.Key, ErrDuplicateEnvKey)
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
func validateExports(scope string, exports []Export) error {
	seen := make(map[string]struct{}, len(exports))
	for _, e := range exports {
		if e.Path == "" {
			return fmt.Errorf("%s: %w", scope, ErrEmptyExportPath)
		}
		if !strings.HasPrefix(e.Path, "/") {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, ErrRelativeExport)
		}
		if strings.TrimRight(e.Path, "/") == "" {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, ErrRootExport)
		}
		if e.Local == "" {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, ErrEmptyExportLocal)
		}
		if _, ok := seen[e.Path]; ok {
			return fmt.Errorf("%s: export %q: %w", scope, e.Path, ErrDuplicateExport)
		}
		seen[e.Path] = struct{}{}
	}
	return nil
}

// validateArtifacts checks artifact fields and that From references a dependency.
//
//revive:disable-next-line:cognitive-complexity validateArtifacts is a linear sequence of field checks; splitting it hurts readability.
func validateArtifacts(scope string, artifacts []Artifact, deps []string) error {
	depSet := make(map[string]struct{}, len(deps))
	for _, d := range deps {
		depSet[d] = struct{}{}
	}
	targetSeen := make(map[string]struct{}, len(artifacts))
	for _, a := range artifacts {
		if a.From == "" {
			return fmt.Errorf("%s: %w", scope, ErrEmptyArtifactFrom)
		}
		if a.Source == "" {
			return fmt.Errorf("%s: artifact from %q: %w", scope, a.From, ErrEmptyArtifactSource)
		}
		if !strings.HasPrefix(a.Source, "/") {
			return fmt.Errorf("%s: artifact source %q: %w", scope, a.Source, ErrRelativeArtifactSource)
		}
		if a.Target == "" {
			return fmt.Errorf("%s: artifact from %q: %w", scope, a.From, ErrEmptyArtifactTarget)
		}
		if _, ok := depSet[a.From]; !ok {
			return fmt.Errorf("%s: artifact from %q: %w", scope, a.From, ErrArtifactNoDep)
		}
		if !strings.HasPrefix(a.Target, "/") {
			return fmt.Errorf("%s: artifact target %q: %w", scope, a.Target, ErrRelativeArtifact)
		}
		if strings.TrimRight(a.Target, "/") == "" {
			return fmt.Errorf("%s: artifact target %q: %w", scope, a.Target, ErrRootArtifact)
		}
		if _, ok := targetSeen[a.Target]; ok {
			return fmt.Errorf("%s: artifact target %q: %w", scope, a.Target, ErrDuplicateArtifact)
		}
		targetSeen[a.Target] = struct{}{}
	}
	return nil
}

// validatePublish checks that a publish declaration has a non-empty image reference.
func validatePublish(scope string, pub *Publish) error {
	if pub == nil {
		return nil
	}
	if strings.TrimSpace(pub.Image) == "" {
		return fmt.Errorf("%s: %w", scope, ErrEmptyPublishImage)
	}
	return nil
}

// validateRetry checks that a retry config is valid.
func validateRetry(scope string, r *Retry) error {
	if r == nil {
		return nil
	}
	if r.Attempts <= 0 {
		return fmt.Errorf("%s: %w", scope, ErrInvalidRetryAttempts)
	}
	if r.Delay < 0 {
		return fmt.Errorf("%s: %w", scope, ErrNegativeDelay)
	}
	switch r.Backoff {
	case "", BackoffNone, BackoffLinear, BackoffExponential:
	default:
		return fmt.Errorf("%s: backoff %q: %w", scope, r.Backoff, ErrInvalidBackoff)
	}
	return nil
}

// validateTimeout checks that a timeout duration is non-negative.
func validateTimeout(scope string, d time.Duration) error {
	if d < 0 {
		return fmt.Errorf("%s: %w", scope, ErrNegativeTimeout)
	}
	return nil
}

// validateShell checks that an explicitly-set shell has at least one argument.
// A nil shell (inherit) is valid; a non-nil empty shell is not.
func validateShell(scope string, s []string) error {
	if s != nil && len(s) == 0 {
		return fmt.Errorf("%s: %w", scope, ErrEmptyShell)
	}
	return nil
}

// checkSelfDeps detects jobs that list themselves as a dependency.
func checkSelfDeps(jobs []Job) error {
	for i := range jobs {
		if slices.Contains(jobs[i].DependsOn, jobs[i].Name) {
			return fmt.Errorf("job %q: %w", jobs[i].Name, ErrSelfDependency)
		}
	}
	return nil
}

// jobGraph provides indexed graph operations over a job slice.
type jobGraph struct {
	jobs  []Job
	index map[string]int
}

// resolveDep looks up a dependency by name, returning a clear error for unknown deps.
func (g *jobGraph) resolveDep(jobName, dep string) (int, error) {
	j, ok := g.index[dep]
	if !ok {
		return 0, fmt.Errorf(
			"job %q depends on %q: %w", jobName, dep, ErrUnknownDep,
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
			return fmt.Errorf("job %q: %w", g.jobs[i].Name, ErrCycleDetected)
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

// ApplyDefaults merges pipeline-wide defaults into each job. Image and workdir
// are filled when the job leaves them empty. Mounts are prepended (defaults
// first, job-specific after). Env is merged with job values winning on conflict.
func ApplyDefaults(jobs []Job, defaults *Defaults) []Job {
	if defaults == nil {
		return mapSlice(jobs, func(j Job) Job { return j.Clone() })
	}
	return mapSlice(jobs, func(j Job) Job {
		c := j.Clone()
		if c.Image == "" {
			c.Image = defaults.Image
		}
		if c.Workdir == "" {
			c.Workdir = defaults.Workdir
		}
		if len(defaults.Mounts) > 0 {
			c.Mounts = append(slices.Clone(defaults.Mounts), c.Mounts...)
		}
		if len(defaults.Env) > 0 {
			c.Env = mergeEnv(defaults.Env, c.Env)
		}
		if c.Shell == nil && len(defaults.Shell) > 0 {
			c.Shell = slices.Clone(defaults.Shell)
		}
		return c
	})
}

// mergeEnv combines base and override env vars. Override values win on key
// conflict. Order is: base entries not overridden, then all override entries.
func mergeEnv(base, override []EnvVar) []EnvVar {
	overrideKeys := make(map[string]struct{}, len(override))
	for _, e := range override {
		overrideKeys[e.Key] = struct{}{}
	}
	merged := make([]EnvVar, 0, len(base)+len(override))
	for _, e := range base {
		if _, ok := overrideKeys[e.Key]; !ok {
			merged = append(merged, e)
		}
	}
	return append(merged, override...)
}

// BuildEnvScope merges pipeline-level and job-level env vars into a flat map
// suitable for conditional.Context.PipelineEnv. Job values win on key conflict.
// For step-level evaluation, callers should additionally merge step.Env into the
// returned map.
func BuildEnvScope(pipelineEnv, jobEnv []EnvVar) map[string]string {
	merged := mergeEnv(pipelineEnv, jobEnv)
	m := make(map[string]string, len(merged))
	for _, e := range merged {
		m[e.Key] = e.Value
	}
	return m
}

// CollectImages extracts unique image references from a pipeline.
func CollectImages(p Pipeline) []string {
	return collectUnique(p.Jobs, func(j Job) string { return j.Image })
}
