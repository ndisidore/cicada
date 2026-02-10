package pipeline

import "fmt"

// StepGroup represents a collection of steps from a single source (inline or
// included file) along with the conflict resolution strategy to apply.
type StepGroup struct {
	Steps      []Step
	Origin     string           // file path for error messages
	OnConflict ConflictStrategy // how to handle name collisions with prior groups
}

// MergeSteps combines step groups in document order, applying per-group
// conflict resolution. Steps from earlier groups take precedence when
// on-conflict is ConflictSkip.
func MergeSteps(groups []StepGroup) ([]Step, error) {
	var n int
	for i := range groups {
		n += len(groups[i].Steps)
	}

	merged := make([]Step, 0, n)
	seen := make(map[string]string, n) // step name -> origin

	for i := range groups {
		g := &groups[i]
		for j := range g.Steps {
			name := g.Steps[j].Name
			if origin, exists := seen[name]; exists {
				switch g.OnConflict {
				case ConflictSkip:
					continue
				default:
					return nil, fmt.Errorf(
						"step %q from %s conflicts with %s: %w",
						name, g.Origin, origin, ErrDuplicateStep,
					)
				}
			}
			seen[name] = g.Origin
			merged = append(merged, g.Steps[j])
		}
	}
	return merged, nil
}

// TerminalSteps returns the names of steps that have no dependents within the
// slice. A terminal step is one whose name does not appear in any other step's
// DependsOn list.
func TerminalSteps(steps []Step) []string {
	depended := make(map[string]struct{})
	for i := range steps {
		for _, dep := range steps[i].DependsOn {
			depended[dep] = struct{}{}
		}
	}

	var terminals []string
	for i := range steps {
		if _, ok := depended[steps[i].Name]; !ok {
			terminals = append(terminals, steps[i].Name)
		}
	}
	return terminals
}

// ExpandAliases replaces alias references in DependsOn fields with the alias's
// terminal step names. It returns an error if an alias name collides with a
// step name in the merged pipeline.
func ExpandAliases(steps []Step, aliases map[string][]string) ([]Step, error) {
	if len(aliases) == 0 {
		return steps, nil
	}

	// Validate: aliases must not collide with step names, unless the alias
	// resolves to exactly itself (single-step fragment with matching name).
	stepNames := make(map[string]struct{}, len(steps))
	for i := range steps {
		stepNames[steps[i].Name] = struct{}{}
	}
	for alias, terminals := range aliases {
		if _, ok := stepNames[alias]; ok {
			if len(terminals) == 1 && terminals[0] == alias {
				continue // no-op alias, no collision
			}
			return nil, fmt.Errorf(
				"alias %q: %w", alias, ErrAliasCollision,
			)
		}
	}

	result := make([]Step, len(steps))
	for i := range steps {
		result[i] = steps[i]
		if len(steps[i].DependsOn) > 0 {
			result[i].DependsOn = expandDeps(steps[i].DependsOn, aliases)
		}
	}
	return result, nil
}

// expandDeps resolves alias references in a dependency list and deduplicates
// while preserving insertion order.
func expandDeps(deps []string, aliases map[string][]string) []string {
	seen := make(map[string]struct{}, len(deps))
	deduped := make([]string, 0, len(deps))
	for _, dep := range deps {
		targets := aliases[dep]
		if targets == nil {
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
