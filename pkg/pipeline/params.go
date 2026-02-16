package pipeline

import "fmt"

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
func SubstituteParams(jobs []Job, params map[string]string) []Job {
	if len(params) == 0 {
		return jobs
	}
	return mapSlice(jobs, func(j Job) Job {
		return substituteJobVars(j, params, "param.")
	})
}

// substituteJobVars applies ${prefix.key} substitution to all string fields
// of a job and its steps. Returns a deep copy with substitutions applied.
func substituteJobVars(j Job, combo map[string]string, prefix string) Job {
	sub := func(v string) string { return substituteVars(v, combo, prefix) }
	c := j.Clone()
	c.Image = sub(c.Image)
	c.Workdir = sub(c.Workdir)
	c.Platform = sub(c.Platform)
	substituteMatrixValues(c.Matrix, sub)
	substituteMounts(c.Mounts, sub)
	substituteCaches(c.Caches, sub)
	substituteEnvValues(c.Env, sub)
	substituteExports(c.Exports, sub)
	substituteArtifacts(c.Artifacts, sub)
	if c.Publish != nil {
		c.Publish.Image = sub(c.Publish.Image)
	}
	if c.When != nil {
		c.When.Expression = sub(c.When.Expression)
		c.When.InvalidateAST()
	}
	for i := range c.Steps {
		substituteStepFields(&c.Steps[i], sub)
	}
	return c
}

// substituteStepFields applies in-place substitution to all string fields of
// a step. The step's slice fields must already be cloned (via Job.Clone).
func substituteStepFields(s *Step, sub func(string) string) {
	for i := range s.Run {
		s.Run[i] = sub(s.Run[i])
	}
	s.Workdir = sub(s.Workdir)
	substituteMounts(s.Mounts, sub)
	substituteCaches(s.Caches, sub)
	substituteEnvValues(s.Env, sub)
	substituteExports(s.Exports, sub)
	substituteArtifacts(s.Artifacts, sub)
	if s.When != nil {
		s.When.Expression = sub(s.When.Expression)
		s.When.InvalidateAST()
	}
}

// substituteMatrixValues applies in-place substitution to all dimension values.
func substituteMatrixValues(m *Matrix, sub func(string) string) {
	if m == nil {
		return
	}
	for i := range m.Dimensions {
		for j := range m.Dimensions[i].Values {
			m.Dimensions[i].Values[j] = sub(m.Dimensions[i].Values[j])
		}
	}
}

// In-place substitution helpers for shared slice field types.

func substituteMounts(mounts []Mount, sub func(string) string) {
	for i := range mounts {
		mounts[i].Source = sub(mounts[i].Source)
		mounts[i].Target = sub(mounts[i].Target)
	}
}

func substituteCaches(caches []Cache, sub func(string) string) {
	for i := range caches {
		caches[i].ID = sub(caches[i].ID)
		caches[i].Target = sub(caches[i].Target)
	}
}

func substituteEnvValues(envs []EnvVar, sub func(string) string) {
	for i := range envs {
		envs[i].Value = sub(envs[i].Value)
	}
}

func substituteExports(exports []Export, sub func(string) string) {
	for i := range exports {
		exports[i].Path = sub(exports[i].Path)
		exports[i].Local = sub(exports[i].Local)
	}
}

func substituteArtifacts(artifacts []Artifact, sub func(string) string) {
	for i := range artifacts {
		artifacts[i].Source = sub(artifacts[i].Source)
		artifacts[i].Target = sub(artifacts[i].Target)
	}
}
