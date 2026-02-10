// Package pipeline defines the core types for a cicada CI/CD pipeline.
package pipeline

import (
	"errors"
	"fmt"
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
)

// _validName matches step names that are safe for use in filesystem paths.
var _validName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// Pipeline represents a CI/CD pipeline parsed from KDL.
type Pipeline struct {
	Name      string
	Steps     []Step
	TopoOrder []int // cached topological order from Validate; nil if not yet validated
}

// Step represents a single execution unit within a pipeline.
type Step struct {
	Name      string
	Image     string
	Run       []string
	Workdir   string
	DependsOn []string
	Mounts    []Mount
	Caches    []Cache
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
	seen := make(map[string]struct{}, len(p.Steps))
	images := make([]string, 0, len(p.Steps))
	for i := range p.Steps {
		ref := p.Steps[i].Image
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		images = append(images, ref)
	}
	return images
}
