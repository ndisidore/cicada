package pipeline

import (
	"maps"
	"slices"
)

// Clone returns a deep copy of the Job. All slice fields and the Matrix
// pointer are cloned to prevent aliasing. Steps are recursively deep-cloned.
func (j Job) Clone() Job {
	j.DependsOn = slices.Clone(j.DependsOn)
	j.Mounts = slices.Clone(j.Mounts)
	j.Caches = slices.Clone(j.Caches)
	j.Env = slices.Clone(j.Env)
	j.Exports = slices.Clone(j.Exports)
	j.Artifacts = slices.Clone(j.Artifacts)
	j.Secrets = slices.Clone(j.Secrets)
	if j.Steps != nil {
		steps := make([]Step, len(j.Steps))
		for i := range j.Steps {
			steps[i] = j.Steps[i].Clone()
		}
		j.Steps = steps
	}
	if j.When != nil {
		w := *j.When
		j.When = &w
	}
	if j.Matrix != nil {
		m := j.Matrix.Clone()
		j.Matrix = &m
	}
	j.MatrixValues = maps.Clone(j.MatrixValues)
	if j.Publish != nil {
		p := *j.Publish
		j.Publish = &p
	}
	if j.Retry != nil {
		r := *j.Retry
		j.Retry = &r
	}
	j.Shell = slices.Clone(j.Shell)
	return j
}

// Clone returns a deep copy of the Step with all slice fields cloned.
func (s Step) Clone() Step {
	s.Run = slices.Clone(s.Run)
	s.Env = slices.Clone(s.Env)
	s.Mounts = slices.Clone(s.Mounts)
	s.Caches = slices.Clone(s.Caches)
	s.Secrets = slices.Clone(s.Secrets)
	s.Exports = slices.Clone(s.Exports)
	s.Artifacts = slices.Clone(s.Artifacts)
	if s.When != nil {
		w := *s.When
		s.When = &w
	}
	s.Shell = slices.Clone(s.Shell)
	return s
}

// Clone returns a deep copy of the Pipeline. All slice fields, the Matrix
// pointer, and Defaults are cloned. Jobs are recursively deep-cloned.
func (p Pipeline) Clone() Pipeline {
	if p.Jobs != nil {
		jobs := make([]Job, len(p.Jobs))
		for i := range p.Jobs {
			jobs[i] = p.Jobs[i].Clone()
		}
		p.Jobs = jobs
	}
	p.Env = slices.Clone(p.Env)
	p.Secrets = slices.Clone(p.Secrets)
	p.TopoOrder = slices.Clone(p.TopoOrder)
	if p.Matrix != nil {
		m := p.Matrix.Clone()
		p.Matrix = &m
	}
	if p.Defaults != nil {
		d := *p.Defaults
		d.Mounts = slices.Clone(d.Mounts)
		d.Env = slices.Clone(d.Env)
		d.Shell = slices.Clone(d.Shell)
		p.Defaults = &d
	}
	return p
}

// Clone returns a deep copy of the Matrix with cloned Dimensions and Values.
func (m Matrix) Clone() Matrix {
	m.Dimensions = slices.Clone(m.Dimensions)
	for i := range m.Dimensions {
		m.Dimensions[i].Values = slices.Clone(m.Dimensions[i].Values)
	}
	return m
}
