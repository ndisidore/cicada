package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func testStep() Step {
	return Step{
		Name:         "build",
		Run:          []string{"go build", "go test"},
		Workdir:      "/app",
		NoCache:      true,
		AllowFailure: true,
		Env:          []EnvVar{{Key: "GO", Value: "1.22"}},
		Mounts:       []Mount{{Source: "/src", Target: "/dst"}},
		Caches:       []Cache{{ID: "go-mod", Target: "/go/pkg"}},
		Exports:      []Export{{Path: "/out", Local: "./out"}},
		Artifacts:    []Artifact{{From: "dep", Source: "/bin", Target: "/app/bin"}},
		Retry:        &Retry{Attempts: 3, Backoff: BackoffExponential},
	}
}

func testJob() Job {
	return Job{
		Name:      "test",
		Image:     "golang:1.22",
		Workdir:   "/app",
		Platform:  "linux/amd64",
		NoCache:   true,
		DependsOn: []string{"build"},
		Mounts:    []Mount{{Source: "/src", Target: "/dst"}},
		Caches:    []Cache{{ID: "go-mod", Target: "/go/pkg"}},
		Env:       []EnvVar{{Key: "GO", Value: "1.22"}},
		Exports:   []Export{{Path: "/out", Local: "./out"}},
		Artifacts: []Artifact{{From: "build", Source: "/bin", Target: "/app/bin"}},
		Matrix: &Matrix{
			Dimensions: []Dimension{{Name: "os", Values: []string{"linux", "darwin"}}},
		},
		Publish: &Publish{Image: "ghcr.io/user/app:latest", Push: true, Insecure: false},
		Steps: []Step{
			{Name: "s1", Run: []string{"echo hello"}, Env: []EnvVar{{Key: "K", Value: "V"}}},
		},
	}
}

func testPipeline() Pipeline {
	return Pipeline{
		Name:      "ci",
		Env:       []EnvVar{{Key: "CI", Value: "true"}},
		TopoOrder: []int{0, 1},
		Matrix: &Matrix{
			Dimensions: []Dimension{{Name: "os", Values: []string{"linux"}}},
		},
		Defaults: &Defaults{
			Image:   "golang:1.22",
			Workdir: "/app",
			Mounts:  []Mount{{Source: "/src", Target: "/dst"}},
			Env:     []EnvVar{{Key: "GO", Value: "1.22"}},
		},
		Jobs: []Job{
			{
				Name:  "build",
				Image: "golang:1.22",
				Steps: []Step{{Name: "s1", Run: []string{"go build"}}},
			},
		},
	}
}

func TestStepClone(t *testing.T) {
	t.Parallel()

	t.Run("values equal", func(t *testing.T) {
		t.Parallel()
		orig := testStep()
		assert.Equal(t, orig, orig.Clone())
	})

	t.Run("nil slices stay nil", func(t *testing.T) {
		t.Parallel()
		clone := Step{Name: "empty"}.Clone()
		assert.Nil(t, clone.Run)
		assert.Nil(t, clone.Env)
		assert.Nil(t, clone.Mounts)
		assert.Nil(t, clone.Caches)
		assert.Nil(t, clone.Exports)
		assert.Nil(t, clone.Artifacts)
	})

	tests := []struct {
		name   string
		mutate func(*Step)
		check  func(*testing.T, Step)
	}{
		{
			name:   "Run independent",
			mutate: func(s *Step) { s.Run[0] = "CHANGED" },
			check: func(t *testing.T, orig Step) {
				t.Helper()
				assert.Equal(t, "go build", orig.Run[0])
			},
		},
		{
			name:   "Env independent",
			mutate: func(s *Step) { s.Env[0].Key = "CHANGED" },
			check: func(t *testing.T, orig Step) {
				t.Helper()
				assert.Equal(t, "GO", orig.Env[0].Key)
			},
		},
		{
			name:   "Mounts independent",
			mutate: func(s *Step) { s.Mounts[0].Source = "CHANGED" },
			check: func(t *testing.T, orig Step) {
				t.Helper()
				assert.Equal(t, "/src", orig.Mounts[0].Source)
			},
		},
		{
			name:   "Caches independent",
			mutate: func(s *Step) { s.Caches[0].ID = "CHANGED" },
			check: func(t *testing.T, orig Step) {
				t.Helper()
				assert.Equal(t, "go-mod", orig.Caches[0].ID)
			},
		},
		{
			name:   "Exports independent",
			mutate: func(s *Step) { s.Exports[0].Path = "CHANGED" },
			check: func(t *testing.T, orig Step) {
				t.Helper()
				assert.Equal(t, "/out", orig.Exports[0].Path)
			},
		},
		{
			name:   "Artifacts independent",
			mutate: func(s *Step) { s.Artifacts[0].From = "CHANGED" },
			check: func(t *testing.T, orig Step) {
				t.Helper()
				assert.Equal(t, "dep", orig.Artifacts[0].From)
			},
		},
		{
			name:   "Retry independent",
			mutate: func(s *Step) { s.Retry.Attempts = 99 },
			check: func(t *testing.T, orig Step) {
				t.Helper()
				assert.Equal(t, 3, orig.Retry.Attempts)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := testStep()
			clone := orig.Clone()
			tt.mutate(&clone)
			tt.check(t, orig)
		})
	}
}

func TestJobClone(t *testing.T) {
	t.Parallel()

	t.Run("values equal", func(t *testing.T) {
		t.Parallel()
		orig := testJob()
		assert.Equal(t, orig, orig.Clone())
	})

	t.Run("nil fields stay nil", func(t *testing.T) {
		t.Parallel()
		clone := Job{Name: "min", Image: "img"}.Clone()
		assert.Nil(t, clone.Matrix)
		assert.Nil(t, clone.Publish)
		assert.Nil(t, clone.Steps)
		assert.Nil(t, clone.DependsOn)
	})

	tests := []struct {
		name   string
		mutate func(*Job)
		check  func(*testing.T, Job)
	}{
		{
			name:   "DependsOn independent",
			mutate: func(j *Job) { j.DependsOn[0] = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "build", orig.DependsOn[0])
			},
		},
		{
			name:   "Mounts independent",
			mutate: func(j *Job) { j.Mounts[0].Source = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "/src", orig.Mounts[0].Source)
			},
		},
		{
			name:   "Env independent",
			mutate: func(j *Job) { j.Env[0].Key = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "GO", orig.Env[0].Key)
			},
		},
		{
			name:   "Caches independent",
			mutate: func(j *Job) { j.Caches[0].ID = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "go-mod", orig.Caches[0].ID)
			},
		},
		{
			name:   "Exports independent",
			mutate: func(j *Job) { j.Exports[0].Path = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "/out", orig.Exports[0].Path)
			},
		},
		{
			name:   "Artifacts independent",
			mutate: func(j *Job) { j.Artifacts[0].From = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "build", orig.Artifacts[0].From)
			},
		},
		{
			name: "steps deep-cloned",
			mutate: func(j *Job) {
				j.Steps[0].Run[0] = "CHANGED"
				j.Steps[0].Env[0].Key = "CHANGED"
			},
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "echo hello", orig.Steps[0].Run[0])
				assert.Equal(t, "K", orig.Steps[0].Env[0].Key)
			},
		},
		{
			name:   "matrix deep-cloned",
			mutate: func(j *Job) { j.Matrix.Dimensions[0].Values[0] = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "linux", orig.Matrix.Dimensions[0].Values[0])
			},
		},
		{
			name:   "publish deep-cloned",
			mutate: func(j *Job) { j.Publish.Image = "CHANGED" },
			check: func(t *testing.T, orig Job) {
				t.Helper()
				assert.Equal(t, "ghcr.io/user/app:latest", orig.Publish.Image)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := testJob()
			clone := orig.Clone()
			tt.mutate(&clone)
			tt.check(t, orig)
		})
	}
}

func TestPipelineClone(t *testing.T) {
	t.Parallel()

	t.Run("values equal", func(t *testing.T) {
		t.Parallel()
		orig := testPipeline()
		assert.Equal(t, orig, orig.Clone())
	})

	t.Run("nil fields stay nil", func(t *testing.T) {
		t.Parallel()
		clone := Pipeline{Name: "bare"}.Clone()
		assert.Nil(t, clone.Matrix)
		assert.Nil(t, clone.Defaults)
		assert.Nil(t, clone.Jobs)
		assert.Nil(t, clone.Env)
		assert.Nil(t, clone.TopoOrder)
	})

	tests := []struct {
		name   string
		mutate func(*Pipeline)
		check  func(*testing.T, Pipeline)
	}{
		{
			name: "jobs deep-cloned",
			mutate: func(p *Pipeline) {
				p.Jobs[0].Image = "CHANGED"
				p.Jobs[0].Steps[0].Run[0] = "CHANGED"
			},
			check: func(t *testing.T, orig Pipeline) {
				t.Helper()
				assert.Equal(t, "golang:1.22", orig.Jobs[0].Image)
				assert.Equal(t, "go build", orig.Jobs[0].Steps[0].Run[0])
			},
		},
		{
			name:   "env independent",
			mutate: func(p *Pipeline) { p.Env[0].Key = "CHANGED" },
			check: func(t *testing.T, orig Pipeline) {
				t.Helper()
				assert.Equal(t, "CI", orig.Env[0].Key)
			},
		},
		{
			name:   "topo order independent",
			mutate: func(p *Pipeline) { p.TopoOrder[0] = 99 },
			check: func(t *testing.T, orig Pipeline) {
				t.Helper()
				assert.Equal(t, 0, orig.TopoOrder[0])
			},
		},
		{
			name:   "matrix deep-cloned",
			mutate: func(p *Pipeline) { p.Matrix.Dimensions[0].Values[0] = "CHANGED" },
			check: func(t *testing.T, orig Pipeline) {
				t.Helper()
				assert.Equal(t, "linux", orig.Matrix.Dimensions[0].Values[0])
			},
		},
		{
			name: "defaults deep-cloned",
			mutate: func(p *Pipeline) {
				p.Defaults.Image = "CHANGED"
				p.Defaults.Mounts[0].Source = "CHANGED"
				p.Defaults.Env[0].Key = "CHANGED"
			},
			check: func(t *testing.T, orig Pipeline) {
				t.Helper()
				assert.Equal(t, "golang:1.22", orig.Defaults.Image)
				assert.Equal(t, "/src", orig.Defaults.Mounts[0].Source)
				assert.Equal(t, "GO", orig.Defaults.Env[0].Key)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := testPipeline()
			clone := orig.Clone()
			tt.mutate(&clone)
			tt.check(t, orig)
		})
	}
}

func TestMatrixClone(t *testing.T) {
	t.Parallel()

	t.Run("values equal", func(t *testing.T) {
		t.Parallel()
		orig := Matrix{
			Dimensions: []Dimension{
				{Name: "os", Values: []string{"linux", "darwin"}},
				{Name: "arch", Values: []string{"amd64"}},
			},
		}
		assert.Equal(t, orig, orig.Clone())
	})

	t.Run("empty matrix", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, Matrix{}.Clone().Dimensions)
	})

	tests := []struct {
		name   string
		mutate func(*Matrix)
		check  func(*testing.T, Matrix)
	}{
		{
			name:   "values independent",
			mutate: func(m *Matrix) { m.Dimensions[0].Values[0] = "CHANGED" },
			check: func(t *testing.T, orig Matrix) {
				t.Helper()
				assert.Equal(t, "linux", orig.Dimensions[0].Values[0])
			},
		},
		{
			name:   "dimension names independent",
			mutate: func(m *Matrix) { m.Dimensions[1].Name = "CHANGED" },
			check: func(t *testing.T, orig Matrix) {
				t.Helper()
				assert.Equal(t, "arch", orig.Dimensions[1].Name)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := Matrix{
				Dimensions: []Dimension{
					{Name: "os", Values: []string{"linux", "darwin"}},
					{Name: "arch", Values: []string{"amd64"}},
				},
			}
			clone := orig.Clone()
			tt.mutate(&clone)
			tt.check(t, orig)
		})
	}
}
