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
)

// Sentinel errors for pipeline validation.
var (
	ErrEmptyPipeline  = errors.New("pipeline has no steps")
	ErrEmptyStepName  = errors.New("step has empty name")
	ErrDuplicateStep  = errors.New("duplicate step name")
	ErrInvalidName    = errors.New("step name contains invalid characters")
	ErrMissingImage   = errors.New("step missing image")
	ErrMissingRun     = errors.New("step has no run commands")
	ErrSelfDependency = errors.New("step depends on itself")
	ErrUnknownDep     = errors.New("unknown dependency")
	ErrCycleDetected  = errors.New("dependency cycle detected")
	ErrEmptyMatrix    = errors.New("matrix has no dimensions")
	ErrEmptyDimension = errors.New("dimension has no values")
	ErrInvalidDimName = errors.New("invalid dimension name")
	ErrDuplicateDim   = errors.New("duplicate dimension name")
	ErrMatrixTooLarge = errors.New("matrix produces too many combinations")
)

// Sentinel errors for modular configuration (includes, fragments, params).
var (
	ErrCircularInclude  = errors.New("circular include detected")
	ErrIncludeDepth     = errors.New("include depth limit exceeded")
	ErrMissingParam     = errors.New("missing required parameter")
	ErrUnknownParam     = errors.New("unknown parameter")
	ErrDuplicateParam   = errors.New("duplicate parameter name")
	ErrDuplicateAlias   = errors.New("duplicate include alias")
	ErrAliasCollision   = errors.New("alias collides with step name")
	ErrInvalidConflict  = errors.New("invalid on-conflict value")
	ErrPipelineNoParams = errors.New("pipeline does not accept parameters")
)

// Sentinel errors for env vars, exports, and artifacts.
var (
	ErrEmptyEnvKey            = errors.New("env key is empty")
	ErrEmptyExportPath        = errors.New("export path is empty")
	ErrDuplicateExport        = errors.New("duplicate export path")
	ErrArtifactNoDep          = errors.New("artifact references step not in depends-on")
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
)

// ConflictStrategy determines behavior when step names collide during merge.
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

// Fragment is a reusable collection of steps with optional parameters.
type Fragment struct {
	Name   string
	Params []ParamDef
	Steps  []Step
}

// _validName matches step names: alphanumeric base with an optional bracket
// suffix for expanded matrix step names (e.g. "build[platform=linux/amd64]").
// The bracket portion permits '/' and ':' for OCI platform specifiers; these
// names are used as map keys and display labels, not raw filesystem paths.
var _validName = regexp.MustCompile(`^[a-zA-Z0-9_-]+(\[[a-zA-Z0-9_.:/\-]+(=[a-zA-Z0-9_.:/\-]+)?(,[a-zA-Z0-9_.:/\-]+(=[a-zA-Z0-9_.:/\-]+)?)*\])?$`)

// _validDimName matches dimension names: alphanumeric, hyphens, underscores.
var _validDimName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Matrix defines a set of dimensions whose cartesian product generates
// concrete step variants during expansion.
type Matrix struct {
	Dimensions []Dimension
}

// Dimension is a single axis in a Matrix with one or more string values.
type Dimension struct {
	Name   string
	Values []string
}

// Combinations returns the cartesian product of all dimensions as a slice of
// maps. Each map maps dimension name to a single value. Returns (nil, nil) for
// an empty matrix and (nil, ErrMatrixTooLarge) on integer overflow.
func (m Matrix) Combinations() ([]map[string]string, error) {
	if len(m.Dimensions) == 0 {
		return nil, nil
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

// Export declares a container path that a step produces as output.
type Export struct {
	Path  string // absolute container path to export
	Local string // host path for local export
}

// Artifact imports a file from a dependency step into this step's container.
type Artifact struct {
	From   string // dependency step name
	Source string // path inside dependency container
	Target string // absolute path inside this step's container
}

// Pipeline represents a CI/CD pipeline parsed from KDL.
type Pipeline struct {
	Name      string
	Steps     []Step
	Env       []EnvVar // pipeline-level env vars inherited by all steps
	Matrix    *Matrix  // pipeline-level matrix; nil if not set
	TopoOrder []int    // cached topological order from Validate; nil if not yet validated
}

// Step represents a single execution unit within a pipeline.
type Step struct {
	Name      string
	Image     string
	Run       []string
	Workdir   string
	Platform  string // OCI platform specifier (e.g. "linux/amd64"); empty means default
	DependsOn []string
	Mounts    []Mount
	Caches    []Cache
	Env       []EnvVar   // step-level env vars (override pipeline-level)
	Exports   []Export   // container paths this step produces
	Artifacts []Artifact // files imported from dependency steps
	Matrix    *Matrix    // step-level matrix; nil if not set
}

// Mount represents a bind mount from host to container.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// Cache represents a persistent cache volume.
type Cache struct {
	ID     string
	Target string
}

// Validate checks that the pipeline is well-formed and returns step indices
// in topological order (dependencies first) on success.
// A single 3-state DFS handles unknown-dep, cycle, and ordering in one pass.
func (p *Pipeline) Validate() ([]int, error) {
	if len(p.Steps) == 0 {
		return nil, ErrEmptyPipeline
	}

	if err := validateEnvVars("pipeline", p.Env); err != nil {
		return nil, err
	}

	g := stepGraph{
		steps: p.Steps,
		index: make(map[string]int, len(p.Steps)),
	}

	for i := range p.Steps {
		if err := validateStep(i, &p.Steps[i], &g); err != nil {
			return nil, err
		}
	}

	if err := checkSelfDeps(p.Steps); err != nil {
		return nil, err
	}

	order, err := g.topoSort()
	if err != nil {
		return nil, err
	}
	p.TopoOrder = order
	return order, nil
}

// validateStep checks a single step for name, image, and run validity.
func validateStep(idx int, s *Step, g *stepGraph) error {
	if s.Name == "" {
		return fmt.Errorf("step at index %d: %w", idx, ErrEmptyStepName)
	}
	if !_validName.MatchString(s.Name) {
		return fmt.Errorf("step %q: %w (must match %s)", s.Name, ErrInvalidName, _validName)
	}
	if _, exists := g.index[s.Name]; exists {
		return fmt.Errorf("step %q: %w", s.Name, ErrDuplicateStep)
	}
	g.index[s.Name] = idx
	if s.Image == "" {
		return fmt.Errorf("step %q: %w", s.Name, ErrMissingImage)
	}
	if len(s.Run) == 0 {
		return fmt.Errorf("step %q: %w", s.Name, ErrMissingRun)
	}
	for _, cmd := range s.Run {
		if strings.TrimSpace(cmd) == "" {
			return fmt.Errorf("step %q: %w", s.Name, ErrMissingRun)
		}
	}
	if err := validateEnvVars(fmt.Sprintf("step %q", s.Name), s.Env); err != nil {
		return err
	}
	if err := validateExports(s.Name, s.Exports); err != nil {
		return err
	}
	return validateArtifacts(s.Name, s.Artifacts, s.DependsOn)
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
// (trailing /) are valid. Export.Local format is intentionally not validated —
// relative paths are resolved against the working directory at runtime by the
// runner.
func validateExports(stepName string, exports []Export) error {
	seen := make(map[string]struct{}, len(exports))
	for _, e := range exports {
		if e.Path == "" {
			return fmt.Errorf("step %q: %w", stepName, ErrEmptyExportPath)
		}
		if !strings.HasPrefix(e.Path, "/") {
			return fmt.Errorf("step %q: export %q: %w", stepName, e.Path, ErrRelativeExport)
		}
		if strings.TrimRight(e.Path, "/") == "" {
			return fmt.Errorf("step %q: export %q: %w", stepName, e.Path, ErrRootExport)
		}
		if e.Local == "" {
			return fmt.Errorf("step %q: export %q: %w", stepName, e.Path, ErrEmptyExportLocal)
		}
		if _, ok := seen[e.Path]; ok {
			return fmt.Errorf("step %q: export %q: %w", stepName, e.Path, ErrDuplicateExport)
		}
		seen[e.Path] = struct{}{}
	}
	return nil
}

// validateArtifacts checks artifact fields and that From references a dependency.
//
//revive:disable-next-line:cognitive-complexity validateArtifacts is a linear sequence of field checks; splitting it hurts readability.
func validateArtifacts(stepName string, artifacts []Artifact, deps []string) error {
	depSet := make(map[string]struct{}, len(deps))
	for _, d := range deps {
		depSet[d] = struct{}{}
	}
	targetSeen := make(map[string]struct{}, len(artifacts))
	for _, a := range artifacts {
		if a.From == "" {
			return fmt.Errorf("step %q: %w", stepName, ErrEmptyArtifactFrom)
		}
		if a.Source == "" {
			return fmt.Errorf("step %q: artifact from %q: %w", stepName, a.From, ErrEmptyArtifactSource)
		}
		if !strings.HasPrefix(a.Source, "/") {
			return fmt.Errorf("step %q: artifact source %q: %w", stepName, a.Source, ErrRelativeArtifactSource)
		}
		if a.Target == "" {
			return fmt.Errorf("step %q: artifact from %q: %w", stepName, a.From, ErrEmptyArtifactTarget)
		}
		if _, ok := depSet[a.From]; !ok {
			return fmt.Errorf("step %q: artifact from %q: %w", stepName, a.From, ErrArtifactNoDep)
		}
		if !strings.HasPrefix(a.Target, "/") {
			return fmt.Errorf("step %q: artifact target %q: %w", stepName, a.Target, ErrRelativeArtifact)
		}
		if strings.TrimRight(a.Target, "/") == "" {
			return fmt.Errorf("step %q: artifact target %q: %w", stepName, a.Target, ErrRootArtifact)
		}
		if _, ok := targetSeen[a.Target]; ok {
			return fmt.Errorf("step %q: artifact target %q: %w", stepName, a.Target, ErrDuplicateArtifact)
		}
		targetSeen[a.Target] = struct{}{}
	}
	return nil
}

// checkSelfDeps detects steps that list themselves as a dependency.
func checkSelfDeps(steps []Step) error {
	for i := range steps {
		if slices.Contains(steps[i].DependsOn, steps[i].Name) {
			return fmt.Errorf(
				"step %q depends on itself: %w", steps[i].Name, ErrSelfDependency,
			)
		}
	}
	return nil
}

// TopoSort returns step indices in topological order (dependencies first).
// It uses a 3-state DFS and returns ErrUnknownDep for missing dependencies
// or ErrCycleDetected when a dependency cycle is found.
func (p *Pipeline) TopoSort() ([]int, error) {
	g := newStepGraph(p.Steps)
	return g.topoSort()
}

// stepGraph provides indexed graph operations over a step slice.
type stepGraph struct {
	steps []Step
	index map[string]int
}

func newStepGraph(steps []Step) stepGraph {
	idx := make(map[string]int, len(steps))
	for i := range steps {
		idx[steps[i].Name] = i
	}
	return stepGraph{steps: steps, index: idx}
}

// resolveDep looks up a dependency by name, returning a clear error for unknown deps.
func (g *stepGraph) resolveDep(stepName, dep string) (int, error) {
	j, ok := g.index[dep]
	if !ok {
		return 0, fmt.Errorf(
			"step %q depends on %q: %w", stepName, dep, ErrUnknownDep,
		)
	}
	return j, nil
}

func (g *stepGraph) topoSort() ([]int, error) {
	const (
		unvisited = iota
		visiting
		visited
	)

	state := make([]int, len(g.steps))
	order := make([]int, 0, len(g.steps))

	var visit func(int) error
	visit = func(i int) error {
		switch state[i] {
		case visited:
			return nil
		case visiting:
			return fmt.Errorf("step %q: %w", g.steps[i].Name, ErrCycleDetected)
		}
		state[i] = visiting
		for _, dep := range g.steps[i].DependsOn {
			j, err := g.resolveDep(g.steps[i].Name, dep)
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

	for i := range g.steps {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// CollectImages extracts unique image references from a pipeline.
func CollectImages(p Pipeline) []string {
	return collectUnique(p.Steps, func(s Step) string { return s.Image })
}
