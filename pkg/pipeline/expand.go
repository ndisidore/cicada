package pipeline

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Expand performs two-phase matrix expansion on a pipeline.
// Phase 1 expands pipeline-level matrices (correlated deps across jobs).
// Phase 2 expands job-level matrices (all-variant deps).
// Returns the original pipeline unchanged if no matrices are present.
func Expand(p Pipeline) (Pipeline, error) {
	if p.Matrix == nil && !slices.ContainsFunc(p.Jobs, func(j Job) bool { return j.Matrix != nil }) {
		return p, nil
	}

	expanded, err := expandPipeline(p)
	if err != nil {
		return Pipeline{}, err
	}

	expanded, err = expandJobs(expanded)
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

	if err := checkDimCollisions(p.Matrix, p.Jobs); err != nil {
		return Pipeline{}, err
	}

	combos, err := p.Matrix.Combinations()
	if err != nil {
		return Pipeline{}, fmt.Errorf("pipeline matrix: %w", err)
	}
	savedJobs := p.Jobs
	p.Jobs = nil
	expanded := p.Clone()
	p.Jobs = savedJobs
	expanded.Matrix = nil
	expanded.Jobs = make([]Job, 0, len(savedJobs)*len(combos))

	for _, combo := range combos {
		for i := range p.Jobs {
			job := replicateJob(&p.Jobs[i], combo)
			job.Name = expandedName(p.Jobs[i].Name, combo)
			job.MatrixValues = maps.Clone(combo)
			job.DependsOn = correlateDeps(p.Jobs[i].DependsOn, combo)
			correlateArtifactFroms(job.Artifacts, combo)
			for j := range job.Caches {
				job.Caches[j].ID = matrixCacheID(job.Caches[j].ID, job.Name)
			}
			// Correlate step-level artifact refs and namespace step-level caches.
			for si := range job.Steps {
				correlateArtifactFroms(job.Steps[si].Artifacts, combo)
				for ci := range job.Steps[si].Caches {
					job.Steps[si].Caches[ci].ID = matrixCacheID(job.Steps[si].Caches[ci].ID, job.Name)
				}
			}
			expanded.Jobs = append(expanded.Jobs, job)
		}
	}

	return expanded, nil
}

// expandJobs handles phase 2: job-level matrix expansion with
// all-variant dependency rewriting.
//
//revive:disable-next-line:cognitive-complexity expandJobs is a linear expansion pipeline; splitting it hurts readability.
func expandJobs(p Pipeline) (Pipeline, error) {
	if !slices.ContainsFunc(p.Jobs, func(j Job) bool { return j.Matrix != nil }) {
		return p, nil
	}

	// Build expansion map: original job name -> all expanded variant names.
	expansionMap := make(map[string][]string)
	var expanded []Job

	for i := range p.Jobs {
		job := &p.Jobs[i]
		if job.Matrix == nil {
			expansionMap[job.Name] = []string{job.Name}
			r := replicateJob(job, nil)
			r.MatrixValues = maps.Clone(job.MatrixValues)
			expanded = append(expanded, r)
			continue
		}

		if err := ValidateMatrix(job.Matrix); err != nil {
			return Pipeline{}, fmt.Errorf("job %q matrix: %w", job.Name, err)
		}

		combos, err := job.Matrix.Combinations()
		if err != nil {
			return Pipeline{}, fmt.Errorf("job %q matrix: %w", job.Name, err)
		}
		names := make([]string, 0, len(combos))
		for _, combo := range combos {
			variant := replicateJob(job, combo)
			variant.Name = expandedName(job.Name, combo)
			variant.Matrix = nil
			variant.MatrixValues = mergeMatrixValues(job.MatrixValues, combo)
			for j := range variant.Caches {
				variant.Caches[j].ID = matrixCacheID(variant.Caches[j].ID, variant.Name)
			}
			for si := range variant.Steps {
				for ci := range variant.Steps[si].Caches {
					variant.Steps[si].Caches[ci].ID = matrixCacheID(variant.Steps[si].Caches[ci].ID, variant.Name)
				}
			}
			names = append(names, variant.Name)
			expanded = append(expanded, variant)
		}
		expansionMap[job.Name] = names
	}

	// Rewrite dependencies: if B depends on matrix job A, B depends on ALL variants of A.
	// Also rewrite Artifact.From references to match expanded job names.
	for i := range expanded {
		expanded[i].DependsOn = rewriteDeps(expanded[i].DependsOn, expansionMap)
		rewriteArtifactFroms(expanded[i].Artifacts, expansionMap)
		for si := range expanded[i].Steps {
			rewriteArtifactFroms(expanded[i].Steps[si].Artifacts, expansionMap)
		}
	}

	savedJobs := p.Jobs
	p.Jobs = nil
	result := p.Clone()
	p.Jobs = savedJobs
	result.Jobs = expanded
	return result, nil
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
// matrix and any job matrix.
func checkDimCollisions(pm *Matrix, jobs []Job) error {
	pipelineDims := make(map[string]struct{}, len(pm.Dimensions))
	for i := range pm.Dimensions {
		pipelineDims[pm.Dimensions[i].Name] = struct{}{}
	}
	for i := range jobs {
		if jobs[i].Matrix == nil {
			continue
		}
		for j := range jobs[i].Matrix.Dimensions {
			name := jobs[i].Matrix.Dimensions[j].Name
			if _, ok := pipelineDims[name]; ok {
				return fmt.Errorf(
					"job %q dimension %q: %w (also in pipeline matrix)",
					jobs[i].Name, name, ErrDuplicateDim,
				)
			}
		}
	}
	return nil
}

// mergeMatrixValues combines existing matrix values (from phase 1) with new
// combo values (from phase 2). New values win on key conflict. Returns nil
// when both inputs are nil.
func mergeMatrixValues(existing, combo map[string]string) map[string]string {
	if existing == nil && combo == nil {
		return nil
	}
	merged := make(map[string]string, len(existing)+len(combo))
	maps.Copy(merged, existing)
	maps.Copy(merged, combo)
	return merged
}

// replicateJob creates a deep copy of a job with matrix variable substitution
// applied to image, run, workdir, platform, mount source/target, cache target,
// and step fields. Cache IDs are copied verbatim (including any namespace prefix
// from phase 1); callers re-namespace IDs via matrixCacheID after replication.
func replicateJob(j *Job, combo map[string]string) Job {
	return substituteJobVars(*j, combo, "matrix.")
}

// expandedName produces a name with sorted dimension key=value pairs
// in brackets. For names already carrying a bracket suffix (from phase 1),
// new dims are merged and the suffix is regenerated.
func expandedName(base string, combo map[string]string) string {
	// Parse any existing bracket suffix.
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
	slices.Sort(keys)

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

// substituteVars replaces ${<prefix>key} placeholders with their values in a
// single pass to avoid chain-substitution when values contain placeholders.
func substituteVars(s string, combo map[string]string, prefix string) string {
	if len(combo) == 0 {
		return s
	}
	pairs := make([]string, 0, len(combo)*2)
	for k, v := range combo {
		pairs = append(pairs, "${"+prefix+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// matrixCacheID namespaces a cache ID with the expanded name to prevent
// cross-variant cache collisions.
func matrixCacheID(baseID, expandedJobName string) string {
	return baseID + "--" + expandedJobName
}

// correlateDeps rewrites dependencies to their same-replica (correlated) names.
// Used in phase 1 where test[os=linux] should depend on build[os=linux].
func correlateDeps(deps []string, combo map[string]string) []string {
	return mapSlice(deps, func(dep string) string { return expandedName(dep, combo) })
}

// rewriteDeps replaces each dependency with all of its expanded variants.
// Used in phase 2 where B depends on ALL variants of matrix job A.
func rewriteDeps(deps []string, expansionMap map[string][]string) []string {
	return flatMap(deps, func(dep string) []string {
		if variants, ok := expansionMap[dep]; ok {
			return variants
		}
		return []string{dep}
	})
}

// correlateArtifactFroms rewrites Artifact.From fields to their correlated
// (same-replica) names during pipeline-level matrix expansion.
func correlateArtifactFroms(artifacts []Artifact, combo map[string]string) {
	for i := range artifacts {
		if artifacts[i].From == "" {
			continue
		}
		artifacts[i].From = expandedName(artifacts[i].From, combo)
	}
}

// rewriteArtifactFroms rewrites Artifact.From fields to match expanded job
// names during job-level matrix expansion. Unlike deps (which fan out to all
// variants), artifacts keep a 1:1 mapping. If From resolves to exactly one
// variant, it is rewritten; otherwise it is left as-is (validation will catch
// any issues).
func rewriteArtifactFroms(artifacts []Artifact, expansionMap map[string][]string) {
	for i := range artifacts {
		if artifacts[i].From == "" {
			continue
		}
		if variants, ok := expansionMap[artifacts[i].From]; ok && len(variants) == 1 {
			artifacts[i].From = variants[0]
		}
	}
}
