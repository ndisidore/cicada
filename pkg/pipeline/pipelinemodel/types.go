// Package pipelinemodel defines the core data types for a cicada CI/CD pipeline.
// This package contains pure type definitions with no business logic beyond
// value-level operations (Clone, Combinations). Validation, expansion, filtering,
// and other transformations live in the parent pipeline package.
package pipelinemodel

import (
	"maps"
	"math"
	"time"

	"github.com/ndisidore/cicada/pkg/conditional"
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

// SecretSource identifies how a secret value is obtained.
type SecretSource string

// SecretSource values.
const (
	SecretSourceHostEnv SecretSource = "hostEnv"
	SecretSourceFile    SecretSource = "file"
	SecretSourceCmd     SecretSource = "cmd"
)

// SecretDecl is a pipeline-level secret declaration that describes where
// a secret value comes from. Var is an optional host env var override for
// hostEnv sources (defaults to Name when empty).
type SecretDecl struct {
	Name   string       // unique secret identifier
	Source SecretSource // hostEnv | file | cmd
	Var    string       // host env var name (hostEnv only; defaults to Name)
	Path   string       // host file path (file only)
	Cmd    string       // shell command (cmd only)
}

// SecretRef is a job/step-level reference that injects a declared secret
// into the container. Exactly one of Env or Mount must be set.
type SecretRef struct {
	Name  string // references a SecretDecl.Name
	Env   string // inject as environment variable with this name
	Mount string // inject as file at this absolute path
}

// FilterOpts configures partial pipeline execution.
type FilterOpts struct {
	StartAt   string // run from this job (or job:step) forward
	StopAfter string // run up to and including this job (or job:step)
}

// ConditionResult holds the pipeline after static condition evaluation
// along with the names of jobs and steps that were skipped.
type ConditionResult struct {
	Pipeline     Pipeline
	Skipped      []string            // job names skipped by static conditions
	SkippedSteps map[string][]string // job name -> step names skipped by static conditions
}

// JobGroup represents a collection of jobs from a single source (inline or
// included file) along with the conflict resolution strategy to apply.
type JobGroup struct {
	Jobs       []Job
	Origin     string           // file path for error messages
	OnConflict ConflictStrategy // how to handle name collisions with prior groups
}
