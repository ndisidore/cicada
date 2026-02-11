package pipeline

import (
	"fmt"
	"slices"
)

// ValidateParams checks that all required params are provided and no unknown
// params are passed.
func ValidateParams(defs []ParamDef, provided map[string]string) error {
	known := make(map[string]struct{}, len(defs))
	for i := range defs {
		name := defs[i].Name
		if _, ok := known[name]; ok {
			return fmt.Errorf("param %q: %w", name, ErrDuplicateParam)
		}
		known[name] = struct{}{}
		if defs[i].Required {
			if _, ok := provided[name]; !ok {
				return fmt.Errorf("param %q: %w", name, ErrMissingParam)
			}
		}
	}
	for k := range provided {
		if _, ok := known[k]; !ok {
			return fmt.Errorf("param %q: %w", k, ErrUnknownParam)
		}
	}
	return nil
}

// ResolveParams merges provided values with defaults, returning the final
// param map. Calls ValidateParams internally.
func ResolveParams(defs []ParamDef, provided map[string]string) (map[string]string, error) {
	if err := ValidateParams(defs, provided); err != nil {
		return nil, err
	}
	resolved := make(map[string]string, len(defs))
	for i := range defs {
		if v, ok := provided[defs[i].Name]; ok {
			resolved[defs[i].Name] = v
		} else {
			resolved[defs[i].Name] = defs[i].Default
		}
	}
	return resolved, nil
}

// SubstituteParams replaces ${param.*} placeholders in all step fields.
// Uses strings.NewReplacer for single-pass substitution, matching the
// approach used by substituteVars for ${matrix.*}.
func SubstituteParams(steps []Step, params map[string]string) []Step {
	if len(params) == 0 {
		return steps
	}
	return mapSlice(steps, func(s Step) Step {
		return substituteStepVars(s, params, "param.")
	})
}

// substituteStepVars applies ${prefix.key} substitution to step fields
// (Image, Workdir, Platform, Run, Mounts, Caches, Env values, Exports,
// Artifacts). Name and DependsOn are copied without substitution.
func substituteStepVars(s Step, combo map[string]string, prefix string) Step {
	sub := func(v string) string { return substituteVars(v, combo, prefix) }
	return Step{
		Name:      s.Name,
		Image:     sub(s.Image),
		Workdir:   sub(s.Workdir),
		Platform:  sub(s.Platform),
		Matrix:    s.Matrix,
		Run:       mapSlice(s.Run, sub),
		DependsOn: slices.Clone(s.DependsOn),
		Mounts: mapSlice(s.Mounts, func(m Mount) Mount {
			return Mount{Source: sub(m.Source), Target: sub(m.Target), ReadOnly: m.ReadOnly}
		}),
		Caches: mapSlice(s.Caches, func(c Cache) Cache {
			return Cache{ID: sub(c.ID), Target: sub(c.Target)}
		}),
		Env: mapSlice(s.Env, func(e EnvVar) EnvVar {
			return EnvVar{Key: e.Key, Value: sub(e.Value)}
		}),
		Exports: mapSlice(s.Exports, func(e Export) Export {
			return Export{Path: sub(e.Path), Local: sub(e.Local)}
		}),
		Artifacts: mapSlice(s.Artifacts, func(a Artifact) Artifact {
			return Artifact{From: a.From, Source: sub(a.Source), Target: sub(a.Target)}
		}),
	}
}
