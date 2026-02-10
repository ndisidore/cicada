package pipeline

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Expand performs two-phase matrix expansion on a pipeline.
// Phase 1 expands pipeline-level matrices (correlated deps across steps).
// Phase 2 expands step-level matrices (all-variant deps).
// Returns the original pipeline unchanged if no matrices are present.
func Expand(p Pipeline) (Pipeline, error) {
	if p.Matrix == nil && !slices.ContainsFunc(p.Steps, func(s Step) bool { return s.Matrix != nil }) {
		return p, nil
	}

	expanded, err := expandPipeline(p)
	if err != nil {
		return Pipeline{}, err
	}

	expanded, err = expandSteps(expanded)
	if err != nil {
		return Pipeline{}, err
	}

	return expanded, nil
}

// expandPipeline handles phase 1: pipeline-level matrix expansion with
// correlated dependency rewriting.
func expandPipeline(p Pipeline) (Pipeline, error) {
	if p.Matrix == nil {
		return p, nil
	}

	if err := ValidateMatrix(p.Matrix); err != nil {
		return Pipeline{}, fmt.Errorf("pipeline matrix: %w", err)
	}

	if err := checkDimCollisions(p.Matrix, p.Steps); err != nil {
		return Pipeline{}, err
	}

	combos, err := p.Matrix.Combinations()
	if err != nil {
		return Pipeline{}, fmt.Errorf("pipeline matrix: %w", err)
	}
	expanded := Pipeline{
		Name:  p.Name,
		Steps: make([]Step, 0, len(p.Steps)*len(combos)),
	}

	for _, combo := range combos {
		for i := range p.Steps {
			step := replicateStep(&p.Steps[i], combo)
			step.Name = expandedStepName(p.Steps[i].Name, combo)
			step.DependsOn = correlateDeps(p.Steps[i].DependsOn, combo)
			for j := range step.Caches {
				step.Caches[j].ID = matrixCacheID(step.Caches[j].ID, step.Name)
			}
			expanded.Steps = append(expanded.Steps, step)
		}
	}

	return expanded, nil
}

// expandSteps handles phase 2: step-level matrix expansion with
// all-variant dependency rewriting.
func expandSteps(p Pipeline) (Pipeline, error) {
	if !slices.ContainsFunc(p.Steps, func(s Step) bool { return s.Matrix != nil }) {
		return p, nil
	}

	// Build expansion map: original step name -> all expanded variant names.
	expansionMap := make(map[string][]string)
	var expanded []Step

	for i := range p.Steps {
		step := &p.Steps[i]
		if step.Matrix == nil {
			expansionMap[step.Name] = []string{step.Name}
			expanded = append(expanded, *step)
			continue
		}

		if err := ValidateMatrix(step.Matrix); err != nil {
			return Pipeline{}, fmt.Errorf("step %q matrix: %w", step.Name, err)
		}

		combos, err := step.Matrix.Combinations()
		if err != nil {
			return Pipeline{}, fmt.Errorf("step %q matrix: %w", step.Name, err)
		}
		names := make([]string, 0, len(combos))
		for _, combo := range combos {
			variant := replicateStep(step, combo)
			variant.Name = expandedStepName(step.Name, combo)
			variant.Matrix = nil
			// step.Caches[j].ID may already carry a phase-1 namespace prefix
			// (e.g. "gomod--test[os=linux]"). Applying matrixCacheID again
			// compounds the prefix so each variant gets a unique cache ID
			// (e.g. "gomod--test[os=linux]--test[go-version=1.21,os=linux]").
			for j := range variant.Caches {
				variant.Caches[j].ID = matrixCacheID(variant.Caches[j].ID, variant.Name)
			}
			names = append(names, variant.Name)
			expanded = append(expanded, variant)
		}
		expansionMap[step.Name] = names
	}

	// Rewrite dependencies: if B depends on matrix step A, B depends on ALL variants of A.
	for i := range expanded {
		expanded[i].DependsOn = rewriteDeps(expanded[i].DependsOn, expansionMap)
	}

	return Pipeline{
		Name:  p.Name,
		Steps: expanded,
	}, nil
}

// _maxMatrixCombinations caps the cartesian product to prevent accidental
// combinatorial explosion from adversarial or misconfigured input.
const _maxMatrixCombinations = 10_000

// ValidateMatrix checks that a matrix has at least one dimension with valid
// names, at least one value per dimension, and that the total combination
// count does not exceed _maxMatrixCombinations.
func ValidateMatrix(m *Matrix) error {
	if len(m.Dimensions) == 0 {
		return ErrEmptyMatrix
	}
	seen := make(map[string]struct{}, len(m.Dimensions))
	total := 1
	for i := range m.Dimensions {
		d := &m.Dimensions[i]
		if !_validDimName.MatchString(d.Name) {
			return fmt.Errorf("dimension %q: %w", d.Name, ErrInvalidDimName)
		}
		if _, ok := seen[d.Name]; ok {
			return fmt.Errorf("dimension %q: %w", d.Name, ErrDuplicateDim)
		}
		seen[d.Name] = struct{}{}
		if len(d.Values) == 0 {
			return fmt.Errorf("dimension %q: %w", d.Name, ErrEmptyDimension)
		}
		total *= len(d.Values)
		if total > _maxMatrixCombinations {
			return fmt.Errorf(
				"%d combinations exceeds limit of %d: %w",
				total, _maxMatrixCombinations, ErrMatrixTooLarge,
			)
		}
	}
	return nil
}

// checkDimCollisions ensures no dimension name appears in both the pipeline
// matrix and any step matrix.
func checkDimCollisions(pm *Matrix, steps []Step) error {
	pipelineDims := make(map[string]struct{}, len(pm.Dimensions))
	for i := range pm.Dimensions {
		pipelineDims[pm.Dimensions[i].Name] = struct{}{}
	}
	for i := range steps {
		if steps[i].Matrix == nil {
			continue
		}
		for j := range steps[i].Matrix.Dimensions {
			name := steps[i].Matrix.Dimensions[j].Name
			if _, ok := pipelineDims[name]; ok {
				return fmt.Errorf(
					"step %q dimension %q: %w (also in pipeline matrix)",
					steps[i].Name, name, ErrDuplicateDim,
				)
			}
		}
	}
	return nil
}

// replicateStep creates a deep copy of a step with matrix variable substitution
// applied to image, run, workdir, mount source/target, and cache target.
// Cache IDs are copied verbatim (including any namespace prefix from phase 1);
// callers re-namespace IDs via matrixCacheID after replication.
func replicateStep(s *Step, combo map[string]string) Step {
	sub := func(v string) string { return substituteVars(v, combo) }
	cp := Step{
		Name:      s.Name,
		Image:     sub(s.Image),
		Workdir:   sub(s.Workdir),
		Matrix:    s.Matrix,
		Run:       mapSlice(s.Run, sub),
		DependsOn: mapSlice(s.DependsOn, func(d string) string { return d }),
		Mounts: mapSlice(s.Mounts, func(m Mount) Mount {
			return Mount{Source: sub(m.Source), Target: sub(m.Target), ReadOnly: m.ReadOnly}
		}),
		Caches: mapSlice(s.Caches, func(c Cache) Cache {
			return Cache{ID: sub(c.ID), Target: sub(c.Target)}
		}),
	}
	return cp
}

// expandedStepName produces a step name with sorted dimension key=value pairs
// in brackets. For steps already carrying a bracket suffix (from phase 1),
// new dims are merged and the suffix is regenerated.
func expandedStepName(base string, combo map[string]string) string {
	// Parse any existing bracket suffix. Validate() runs after Expand(), so
	// base may contain an unmatched '[' without a closing ']'. Defensively
	// require both brackets before parsing the suffix.
	existing := make(map[string]string)
	name := base
	if idx := strings.IndexByte(base, '['); idx >= 0 {
		if endIdx := strings.LastIndexByte(base, ']'); endIdx > idx {
			name = base[:idx]
			suffix := base[idx+1 : endIdx]
			for kv := range strings.SplitSeq(suffix, ",") {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) == 2 {
					existing[parts[0]] = parts[1]
				}
			}
		}
	}

	// Merge new combo into existing.
	maps.Copy(existing, combo)

	keys := make([]string, 0, len(existing))
	for k := range existing {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "%s[", name)
	for i, k := range keys {
		if i > 0 {
			_, _ = b.WriteRune(',')
		}
		_, _ = fmt.Fprintf(&b, "%s=%s", k, existing[k])
	}
	_, _ = b.WriteRune(']')
	return b.String()
}

// substituteVars replaces all ${matrix.key} placeholders with their values
// in a single pass to avoid chain-substitution when values contain placeholders.
func substituteVars(s string, combo map[string]string) string {
	if len(combo) == 0 {
		return s
	}
	pairs := make([]string, 0, len(combo)*2)
	for k, v := range combo {
		pairs = append(pairs, "${matrix."+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// matrixCacheID namespaces a cache ID with the expanded step name to prevent
// cross-variant cache collisions.
func matrixCacheID(baseID, expandedName string) string {
	return baseID + "--" + expandedName
}

// correlateDeps rewrites dependencies to their same-replica (correlated) names.
// Used in phase 1 where test[os=linux] should depend on build[os=linux].
func correlateDeps(deps []string, combo map[string]string) []string {
	return mapSlice(deps, func(dep string) string { return expandedStepName(dep, combo) })
}

// rewriteDeps replaces each dependency with all of its expanded variants.
// Used in phase 2 where B depends on ALL variants of matrix step A.
func rewriteDeps(deps []string, expansionMap map[string][]string) []string {
	return flatMap(deps, func(dep string) []string {
		if variants, ok := expansionMap[dep]; ok {
			return variants
		}
		return []string{dep}
	})
}
