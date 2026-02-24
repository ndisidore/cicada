package pipeline

import (
	"fmt"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// MergeJobs combines job groups in document order, applying per-group
// conflict resolution. Jobs from earlier groups take precedence when
// on-conflict is ConflictSkip.
func MergeJobs(groups []pm.JobGroup) ([]pm.Job, error) {
	var n int
	for i := range groups {
		n += len(groups[i].Jobs)
	}

	merged := make([]pm.Job, 0, n)
	seen := make(map[string]string, n) // job name -> origin

	for i := range groups {
		g := &groups[i]
		for j := range g.Jobs {
			name := g.Jobs[j].Name
			if origin, exists := seen[name]; exists {
				switch g.OnConflict {
				case pm.ConflictSkip:
					continue
				default:
					return nil, fmt.Errorf(
						"job %q from %s conflicts with %s: %w",
						name, g.Origin, origin, pm.ErrDuplicateJob,
					)
				}
			}
			seen[name] = g.Origin
			merged = append(merged, g.Jobs[j])
		}
	}
	return merged, nil
}

// TerminalJobs returns the names of jobs that have no dependents within the
// slice. A terminal job is one whose name does not appear in any other job's
// DependsOn list.
func TerminalJobs(jobs []pm.Job) []string {
	depended := make(map[string]struct{})
	for i := range jobs {
		for _, dep := range jobs[i].DependsOn {
			depended[dep] = struct{}{}
		}
	}

	var terminals []string
	for i := range jobs {
		if _, ok := depended[jobs[i].Name]; !ok {
			terminals = append(terminals, jobs[i].Name)
		}
	}
	return terminals
}

// ExpandAliases replaces alias references in DependsOn fields with the alias's
// terminal job names. It returns an error if an alias name collides with a
// job name in the merged pipeline.
func ExpandAliases(jobs []pm.Job, aliases map[string][]string) ([]pm.Job, error) {
	if len(aliases) == 0 {
		return jobs, nil
	}

	// Validate: aliases must not collide with job names, unless the alias
	// resolves to exactly itself (single-job fragment with matching name).
	jobNames := make(map[string]struct{}, len(jobs))
	for i := range jobs {
		jobNames[jobs[i].Name] = struct{}{}
	}
	for alias, terminals := range aliases {
		if _, ok := jobNames[alias]; ok {
			if len(terminals) == 1 && terminals[0] == alias {
				continue // no-op alias, no collision
			}
			return nil, fmt.Errorf(
				"alias %q: %w", alias, pm.ErrAliasCollision,
			)
		}
	}

	result := make([]pm.Job, len(jobs))
	for i := range jobs {
		c := jobs[i].Clone()
		if len(jobs[i].DependsOn) > 0 {
			c.DependsOn = expandDeps(jobs[i].DependsOn, aliases)
		}
		result[i] = c
	}
	return result, nil
}

// expandDeps resolves alias references in a dependency list and deduplicates
// while preserving insertion order.
func expandDeps(deps []string, aliases map[string][]string) []string {
	seen := make(map[string]struct{}, len(deps))
	deduped := make([]string, 0, len(deps))
	for _, dep := range deps {
		targets, isAlias := aliases[dep]
		if !isAlias || len(targets) == 0 {
			targets = []string{dep}
		}
		for _, t := range targets {
			if _, dup := seen[t]; !dup {
				seen[t] = struct{}{}
				deduped = append(deduped, t)
			}
		}
	}
	return deduped
}
