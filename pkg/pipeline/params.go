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

// SubstituteParams replaces ${param.*} placeholders in all job and step fields.
// Uses strings.NewReplacer for single-pass substitution, matching the
// approach used by substituteVars for ${matrix.*}.
func SubstituteParams(jobs []Job, params map[string]string) []Job {
	if len(params) == 0 {
		return jobs
	}
	return mapSlice(jobs, func(j Job) Job {
		return substituteJobVars(j, params, "param.")
	})
}

// substituteJobVars applies ${prefix.key} substitution to job fields
// (Image, Workdir, Platform, Mounts, Caches, Env values, Exports, Artifacts)
// and recursively substitutes in each step's fields. Name and DependsOn are
// copied without substitution.
func substituteJobVars(j Job, combo map[string]string, prefix string) Job {
	sub := func(v string) string { return substituteVars(v, combo, prefix) }
	return Job{
		Name:      j.Name,
		Image:     sub(j.Image),
		Workdir:   sub(j.Workdir),
		Platform:  sub(j.Platform),
		DependsOn: slices.Clone(j.DependsOn),
		Matrix:    substituteMatrix(j.Matrix, sub),
		NoCache:   j.NoCache,
		Mounts: mapSlice(j.Mounts, func(m Mount) Mount {
			return Mount{Source: sub(m.Source), Target: sub(m.Target), ReadOnly: m.ReadOnly}
		}),
		Caches: mapSlice(j.Caches, func(c Cache) Cache {
			return Cache{ID: sub(c.ID), Target: sub(c.Target)}
		}),
		Env: mapSlice(j.Env, func(e EnvVar) EnvVar {
			return EnvVar{Key: e.Key, Value: sub(e.Value)}
		}),
		Exports: mapSlice(j.Exports, func(e Export) Export {
			return Export{Path: sub(e.Path), Local: sub(e.Local)}
		}),
		Artifacts: mapSlice(j.Artifacts, func(a Artifact) Artifact {
			return Artifact{From: a.From, Source: sub(a.Source), Target: sub(a.Target)}
		}),
		Steps: mapSlice(j.Steps, func(s Step) Step {
			return substituteStepVars(s, combo, prefix)
		}),
	}
}

// substituteMatrix applies a substitution function to dimension values in a
// matrix, returning nil for nil input. Dimension names are identifiers and
// are copied unchanged.
func substituteMatrix(m *Matrix, sub func(string) string) *Matrix {
	if m == nil {
		return nil
	}
	dims := make([]Dimension, len(m.Dimensions))
	for i, d := range m.Dimensions {
		dims[i] = Dimension{
			Name:   d.Name,
			Values: mapSlice(d.Values, sub),
		}
	}
	return &Matrix{Dimensions: dims}
}

// substituteStepVars applies ${prefix.key} substitution to step fields
// (Run, Workdir, Mounts, Caches, Env values, Exports, Artifacts).
// Name is copied without substitution.
func substituteStepVars(s Step, combo map[string]string, prefix string) Step {
	sub := func(v string) string { return substituteVars(v, combo, prefix) }
	return Step{
		Name:    s.Name,
		Run:     mapSlice(s.Run, sub),
		Workdir: sub(s.Workdir),
		NoCache: s.NoCache,
		Env: mapSlice(s.Env, func(e EnvVar) EnvVar {
			return EnvVar{Key: e.Key, Value: sub(e.Value)}
		}),
		Mounts: mapSlice(s.Mounts, func(m Mount) Mount {
			return Mount{Source: sub(m.Source), Target: sub(m.Target), ReadOnly: m.ReadOnly}
		}),
		Caches: mapSlice(s.Caches, func(c Cache) Cache {
			return Cache{ID: sub(c.ID), Target: sub(c.Target)}
		}),
		Exports: mapSlice(s.Exports, func(e Export) Export {
			return Export{Path: sub(e.Path), Local: sub(e.Local)}
		}),
		Artifacts: mapSlice(s.Artifacts, func(a Artifact) Artifact {
			return Artifact{From: a.From, Source: sub(a.Source), Target: sub(a.Target)}
		}),
	}
}
