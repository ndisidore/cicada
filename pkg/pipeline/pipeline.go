// Package pipeline provides validation, expansion, filtering, and
// transformation operations for cicada CI/CD pipeline definitions.
// Pure data types live in the pipelinemodel sub-package.
package pipeline

import (
	"regexp"
	"slices"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// _validName matches names: alphanumeric base with an optional bracket
// suffix for expanded matrix names (e.g. "build[platform=linux/amd64]").
// The bracket portion permits '/' and ':' for OCI platform specifiers; these
// names are used as map keys and display labels, not raw filesystem paths.
var _validName = regexp.MustCompile(`^[a-zA-Z0-9_-]+(\[[a-zA-Z0-9_.:/\-]+(=[a-zA-Z0-9_.:/\-]+)?(,[a-zA-Z0-9_.:/\-]+(=[a-zA-Z0-9_.:/\-]+)?)*\])?$`)

// _validDimName matches dimension names: alphanumeric, hyphens, underscores.
var _validDimName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// ApplyDefaults merges pipeline-wide defaults into each job. Image and workdir
// are filled when the job leaves them empty. Mounts are prepended (defaults
// first, job-specific after). Env is merged with job values winning on conflict.
func ApplyDefaults(jobs []pm.Job, defaults *pm.Defaults) []pm.Job {
	if defaults == nil {
		return mapSlice(jobs, func(j pm.Job) pm.Job { return j.Clone() })
	}
	return mapSlice(jobs, func(j pm.Job) pm.Job {
		c := j.Clone()
		if c.Image == "" {
			c.Image = defaults.Image
		}
		if c.Workdir == "" {
			c.Workdir = defaults.Workdir
		}
		if len(defaults.Mounts) > 0 {
			c.Mounts = append(slices.Clone(defaults.Mounts), c.Mounts...)
		}
		if len(defaults.Env) > 0 {
			c.Env = mergeEnv(defaults.Env, c.Env)
		}
		if c.Shell == nil && len(defaults.Shell) > 0 {
			c.Shell = slices.Clone(defaults.Shell)
		}
		return c
	})
}

// mergeEnv combines base and override env vars. Override values win on key
// conflict. Order is: base entries not overridden, then all override entries.
func mergeEnv(base, override []pm.EnvVar) []pm.EnvVar {
	overrideKeys := make(map[string]struct{}, len(override))
	for _, e := range override {
		overrideKeys[e.Key] = struct{}{}
	}
	merged := make([]pm.EnvVar, 0, len(base)+len(override))
	for _, e := range base {
		if _, ok := overrideKeys[e.Key]; !ok {
			merged = append(merged, e)
		}
	}
	return append(merged, override...)
}

// BuildEnvScope merges pipeline-level and job-level env vars into a flat map
// suitable for conditional.Context.PipelineEnv. Job values win on key conflict.
// For step-level evaluation, callers should additionally merge step.Env into the
// returned map.
func BuildEnvScope(pipelineEnv, jobEnv []pm.EnvVar) map[string]string {
	merged := mergeEnv(pipelineEnv, jobEnv)
	m := make(map[string]string, len(merged))
	for _, e := range merged {
		m[e.Key] = e.Value
	}
	return m
}

// CollectImages extracts unique image references from a pipeline.
func CollectImages(p pm.Pipeline) []string {
	return collectUnique(p.Jobs, func(j pm.Job) string { return j.Image })
}
