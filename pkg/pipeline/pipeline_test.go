package pipeline

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/pkg/conditional"
	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

func TestMatrixCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		m       pm.Matrix
		want    []map[string]string
		wantErr error
	}{
		{
			name: "single dimension",
			m: pm.Matrix{
				Dimensions: []pm.Dimension{
					{Name: "os", Values: []string{"linux", "darwin"}},
				},
			},
			want: []map[string]string{
				{"os": "linux"},
				{"os": "darwin"},
			},
		},
		{
			name: "multi dimension cartesian product",
			m: pm.Matrix{
				Dimensions: []pm.Dimension{
					{Name: "os", Values: []string{"linux", "darwin"}},
					{Name: "arch", Values: []string{"amd64", "arm64"}},
				},
			},
			want: []map[string]string{
				{"os": "linux", "arch": "amd64"},
				{"os": "linux", "arch": "arm64"},
				{"os": "darwin", "arch": "amd64"},
				{"os": "darwin", "arch": "arm64"},
			},
		},
		{
			name: "three dimensions",
			m: pm.Matrix{
				Dimensions: []pm.Dimension{
					{Name: "os", Values: []string{"linux"}},
					{Name: "go", Values: []string{"1.21", "1.22"}},
					{Name: "db", Values: []string{"pg", "mysql"}},
				},
			},
			want: []map[string]string{
				{"os": "linux", "go": "1.21", "db": "pg"},
				{"os": "linux", "go": "1.21", "db": "mysql"},
				{"os": "linux", "go": "1.22", "db": "pg"},
				{"os": "linux", "go": "1.22", "db": "mysql"},
			},
		},
		{
			name: "empty matrix",
			m:    pm.Matrix{},
			want: []map[string]string{},
		},
		{
			name: "single value dimension",
			m: pm.Matrix{
				Dimensions: []pm.Dimension{
					{Name: "os", Values: []string{"linux"}},
				},
			},
			want: []map[string]string{
				{"os": "linux"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.m.Combinations()
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCollectImages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		p    pm.Pipeline
		want []string
	}{
		{
			name: "unique images",
			p: pm.Pipeline{
				Jobs: []pm.Job{
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
					{Image: "rust:1.76"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23", "rust:1.76"},
		},
		{
			name: "deduplicates",
			p: pm.Pipeline{
				Jobs: []pm.Job{
					{Image: "alpine:latest"},
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23"},
		},
		{
			name: "empty pipeline",
			p:    pm.Pipeline{},
			want: nil,
		},
		{
			name: "single job",
			p: pm.Pipeline{
				Jobs: []pm.Job{
					{Image: "ubuntu:22.04"},
				},
			},
			want: []string{"ubuntu:22.04"},
		},
		{
			name: "skips empty image refs",
			p: pm.Pipeline{
				Jobs: []pm.Job{
					{Image: "alpine:latest"},
					{Image: ""},
					{Image: "golang:1.23"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CollectImages(tt.p)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateEnvVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     []pm.EnvVar
		wantErr error
	}{
		{
			name: "valid env vars pass",
			env: []pm.EnvVar{
				{Key: "FOO", Value: "bar"},
				{Key: "BAZ", Value: "qux"},
			},
		},
		{
			name: "empty env key returns ErrEmptyEnvKey",
			env: []pm.EnvVar{
				{Key: "", Value: "bar"},
			},
			wantErr: pm.ErrEmptyEnvKey,
		},
		{
			name: "duplicate env key returns ErrDuplicateEnvKey",
			env: []pm.EnvVar{
				{Key: "FOO", Value: "bar"},
				{Key: "FOO", Value: "baz"},
			},
			wantErr: pm.ErrDuplicateEnvKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			p := pm.Pipeline{
				Name: "test-pipeline",
				Jobs: []pm.Job{
					{
						Name:  "build",
						Image: "golang:1.23",
						Env:   tt.env,
						Steps: []pm.Step{{Name: "build", Run: []string{"go build ./..."}}},
					},
				},
			}

			// Act
			_, err := Validate(&p)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidatePipelineEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     []pm.EnvVar
		wantErr error
	}{
		{
			name: "valid pipeline env vars pass",
			env: []pm.EnvVar{
				{Key: "CI", Value: "true"},
				{Key: "REGION", Value: "us-east-1"},
			},
		},
		{
			name:    "empty pipeline env key returns ErrEmptyEnvKey",
			env:     []pm.EnvVar{{Key: "", Value: "bar"}},
			wantErr: pm.ErrEmptyEnvKey,
		},
		{
			name: "duplicate pipeline env key returns ErrDuplicateEnvKey",
			env: []pm.EnvVar{
				{Key: "CI", Value: "true"},
				{Key: "CI", Value: "false"},
			},
			wantErr: pm.ErrDuplicateEnvKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := pm.Pipeline{
				Name: "test-pipeline",
				Env:  tt.env,
				Jobs: []pm.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pm.Step{{Name: "build", Run: []string{"echo hi"}}},
					},
				},
			}

			_, err := Validate(&p)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateExports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		exports []pm.Export
		wantErr error
	}{
		{
			name: "valid file export passes",
			exports: []pm.Export{
				{Path: "/output/build.tar", Local: "./build.tar"},
			},
		},
		{
			name: "valid directory export with trailing slash passes",
			exports: []pm.Export{
				{Path: "/output/dist/", Local: "./dist"},
			},
		},
		{
			name: "empty export path returns ErrEmptyExportPath",
			exports: []pm.Export{
				{Path: ""},
			},
			wantErr: pm.ErrEmptyExportPath,
		},
		{
			name: "relative export path returns ErrRelativeExport",
			exports: []pm.Export{
				{Path: "output/build.tar"},
			},
			wantErr: pm.ErrRelativeExport,
		},
		{
			name: "root export path returns ErrRootExport",
			exports: []pm.Export{
				{Path: "/"},
			},
			wantErr: pm.ErrRootExport,
		},
		{
			name: "root with multiple slashes returns ErrRootExport",
			exports: []pm.Export{
				{Path: "///"},
			},
			wantErr: pm.ErrRootExport,
		},
		{
			name: "empty export local returns ErrEmptyExportLocal",
			exports: []pm.Export{
				{Path: "/output/build.tar", Local: ""},
			},
			wantErr: pm.ErrEmptyExportLocal,
		},
		{
			name: "duplicate export path returns ErrDuplicateExport",
			exports: []pm.Export{
				{Path: "/output/build.tar", Local: "./build.tar"},
				{Path: "/output/build.tar", Local: "./build2.tar"},
			},
			wantErr: pm.ErrDuplicateExport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			p := pm.Pipeline{
				Name: "test-pipeline",
				Jobs: []pm.Job{
					{
						Name:    "build",
						Image:   "golang:1.23",
						Exports: tt.exports,
						Steps:   []pm.Step{{Name: "build", Run: []string{"go build ./..."}}},
					},
				},
			}

			// Act
			_, err := Validate(&p)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateArtifacts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		jobs    []pm.Job
		wantErr error
	}{
		{
			name: "valid artifact passes",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build -o /out/app ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "/out/app", Target: "/app/bin"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
		},
		{
			name: "empty artifact From returns ErrEmptyArtifactFrom",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "", Source: "/out/app", Target: "/app/bin"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrEmptyArtifactFrom,
		},
		{
			name: "empty artifact Source returns ErrEmptyArtifactSource",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "", Target: "/app/bin"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrEmptyArtifactSource,
		},
		{
			name: "empty artifact Target returns ErrEmptyArtifactTarget",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "/out/app", Target: ""},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrEmptyArtifactTarget,
		},
		{
			name: "artifact From not in DependsOn returns ErrArtifactNoDep",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:  "deploy",
					Image: "alpine:latest",
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "/out/app", Target: "/app/bin"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrArtifactNoDep,
		},
		{
			name: "relative artifact source returns ErrRelativeArtifactSource",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "out/app", Target: "/app/bin"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrRelativeArtifactSource,
		},
		{
			name: "relative artifact target returns ErrRelativeArtifact",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "/out/app", Target: "app/bin"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrRelativeArtifact,
		},
		{
			name: "root artifact target returns ErrRootArtifact",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "/out/app", Target: "/"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrRootArtifact,
		},
		{
			name: "root with slashes artifact target returns ErrRootArtifact",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "/out/app", Target: "///"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrRootArtifact,
		},
		{
			name: "duplicate artifact target returns ErrDuplicateArtifact",
			jobs: []pm.Job{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "compile", Run: []string{"go build ./..."}}},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					DependsOn: []string{"compile"},
					Artifacts: []pm.Artifact{
						{From: "compile", Source: "/out/app", Target: "/app/bin"},
						{From: "compile", Source: "/out/lib", Target: "/app/bin"},
					},
					Steps: []pm.Step{{Name: "deploy", Run: []string{"./deploy.sh"}}},
				},
			},
			wantErr: pm.ErrDuplicateArtifact,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			p := pm.Pipeline{
				Name: "test-pipeline",
				Jobs: tt.jobs,
			}

			// Act
			_, err := Validate(&p)

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateStepRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		jobs    []pm.Job
		wantErr error
	}{
		{
			name: "step with nil Run rejected",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{
					{Name: "setup", Run: nil},
					{Name: "compile", Run: []string{"go build"}},
				},
			}},
			wantErr: pm.ErrMissingRun,
		},
		{
			name: "step with empty Run slice rejected",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{
					{Name: "setup", Run: []string{}},
					{Name: "compile", Run: []string{"go build"}},
				},
			}},
			wantErr: pm.ErrMissingRun,
		},
		{
			name: "step with empty-string run command rejected",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{
					{Name: "setup", Run: []string{""}},
				},
			}},
			wantErr: pm.ErrEmptyRunCommand,
		},
		{
			name: "step with whitespace-only run command rejected",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{
					{Name: "setup", Run: []string{"  "}},
				},
			}},
			wantErr: pm.ErrEmptyRunCommand,
		},
		{
			name: "all steps with valid Run passes",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{
					{Name: "setup", Run: []string{"apk add git"}},
					{Name: "compile", Run: []string{"go build"}},
				},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := pm.Pipeline{Name: "test", Jobs: tt.jobs}
			_, err := Validate(&p)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestApplyDefaults(t *testing.T) {
	t.Parallel()

	t.Run("matrix pointer is not aliased", func(t *testing.T) {
		t.Parallel()

		input := []pm.Job{{
			Name:  "test",
			Image: "alpine",
			Steps: []pm.Step{{Name: "test", Run: []string{"echo hi"}}},
			Matrix: &pm.Matrix{Dimensions: []pm.Dimension{
				{Name: "go", Values: []string{"1.22", "1.23"}},
			}},
		}}
		defaults := &pm.Defaults{Image: "golang:1.23"}

		result := ApplyDefaults(input, defaults)
		require.NotNil(t, result[0].Matrix)
		assert.NotSame(t, input[0].Matrix, result[0].Matrix,
			"Matrix pointer must not alias the input")

		result[0].Matrix = nil
		assert.NotNil(t, input[0].Matrix,
			"nilling result Matrix must not affect input")
	})

	t.Run("nil matrix preserved", func(t *testing.T) {
		t.Parallel()

		input := []pm.Job{{
			Name:  "test",
			Image: "alpine",
			Steps: []pm.Step{{Name: "test", Run: []string{"echo hi"}}},
		}}
		defaults := &pm.Defaults{Image: "golang:1.23"}

		result := ApplyDefaults(input, defaults)
		assert.Nil(t, result[0].Matrix)
	})

	t.Run("nil defaults deep-clones jobs", func(t *testing.T) {
		t.Parallel()

		input := []pm.Job{{
			Name:      "test",
			Image:     "alpine",
			DependsOn: []string{"build"},
			Steps:     []pm.Step{{Name: "test", Run: []string{"echo hi"}}},
			Matrix: &pm.Matrix{Dimensions: []pm.Dimension{
				{Name: "go", Values: []string{"1.22"}},
			}},
		}}

		result := ApplyDefaults(input, nil)
		require.Len(t, result, 1)
		assert.Equal(t, input[0], result[0])

		result[0].Name = "mutated"
		assert.Equal(t, "test", input[0].Name,
			"mutating result scalar must not affect input")

		result[0].Steps[0].Run[0] = "CHANGED"
		assert.Equal(t, "echo hi", input[0].Steps[0].Run[0],
			"mutating result Steps must not affect input")

		result[0].DependsOn[0] = "CHANGED"
		assert.Equal(t, "build", input[0].DependsOn[0],
			"mutating result DependsOn must not affect input")

		assert.NotSame(t, input[0].Matrix, result[0].Matrix,
			"Matrix pointer must not alias the input")
	})

	t.Run("env merge job wins on conflict", func(t *testing.T) {
		t.Parallel()

		input := []pm.Job{{
			Name:  "test",
			Image: "alpine",
			Steps: []pm.Step{{Name: "test", Run: []string{"echo hi"}}},
			Env: []pm.EnvVar{
				{Key: "SHARED", Value: "from-job"},
				{Key: "JOB_ONLY", Value: "yes"},
			},
		}}
		defaults := &pm.Defaults{
			Image: "alpine",
			Env: []pm.EnvVar{
				{Key: "SHARED", Value: "from-defaults"},
				{Key: "DEFAULT_ONLY", Value: "yes"},
			},
		}

		result := ApplyDefaults(input, defaults)
		require.Len(t, result, 1)
		assert.Equal(t, []pm.EnvVar{
			{Key: "DEFAULT_ONLY", Value: "yes"},
			{Key: "SHARED", Value: "from-job"},
			{Key: "JOB_ONLY", Value: "yes"},
		}, result[0].Env, "job env should override defaults on conflict")
	})
}

func TestValidatePublish(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		publish *pm.Publish
		wantErr error
	}{
		{
			name:    "nil publish passes",
			publish: nil,
		},
		{
			name:    "valid publish passes",
			publish: &pm.Publish{Image: "ghcr.io/user/app:latest", Push: true},
		},
		{
			name:    "valid publish with insecure passes",
			publish: &pm.Publish{Image: "localhost:5000/app:dev", Push: true, Insecure: true},
		},
		{
			name:    "empty image returns ErrEmptyPublishImage",
			publish: &pm.Publish{Image: "", Push: true},
			wantErr: pm.ErrEmptyPublishImage,
		},
		{
			name:    "whitespace-only image returns ErrEmptyPublishImage",
			publish: &pm.Publish{Image: "   ", Push: true},
			wantErr: pm.ErrEmptyPublishImage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := pm.Pipeline{
				Name: "test-pipeline",
				Jobs: []pm.Job{
					{
						Name:    "build",
						Image:   "golang:1.23",
						Publish: tt.publish,
						Steps:   []pm.Step{{Name: "build", Run: []string{"go build ./..."}}},
					},
				},
			}

			_, err := Validate(&p)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateWhenCondition(t *testing.T) {
	t.Parallel()

	validStep := pm.Step{Name: "s1", Run: []string{"echo ok"}}

	tests := []struct {
		name    string
		jobs    []pm.Job
		wantErr error
	}{
		{
			name: "valid job when passes",
			jobs: []pm.Job{{
				Name:  "deploy",
				Image: "alpine",
				When:  &conditional.When{Expression: `branch == "main"`},
				Steps: []pm.Step{validStep},
			}},
		},
		{
			name: "nil when passes",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{validStep},
			}},
		},
		{
			name: "invalid job when expression rejected",
			jobs: []pm.Job{{
				Name:  "deploy",
				Image: "alpine",
				When:  &conditional.When{Expression: `invalid!!!`},
				Steps: []pm.Step{validStep},
			}},
			wantErr: pm.ErrInvalidCondition,
		},
		{
			name: "valid step when passes",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{{
					Name: "conditional",
					Run:  []string{"echo ok"},
					When: &conditional.When{Expression: `env("CI") == "true"`},
				}},
			}},
		},
		{
			name: "invalid step when expression rejected",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{{
					Name: "bad",
					Run:  []string{"echo"},
					When: &conditional.When{Expression: `invalid!!!`},
				}},
			}},
			wantErr: pm.ErrInvalidCondition,
		},
		{
			name: "step when with output() rejected",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{{
					Name: "deferred",
					Run:  []string{"echo"},
					When: &conditional.When{Expression: `output("check", "ready") == "yes"`},
				}},
			}},
			wantErr: pm.ErrDeferredStepWhen,
		},
		{
			name: "job when with output() allowed",
			jobs: []pm.Job{{
				Name:  "deploy",
				Image: "alpine",
				When:  &conditional.When{Expression: `output("check", "ready") == "yes"`, Deferred: true},
				Steps: []pm.Step{validStep},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := pm.Pipeline{Name: "test", Jobs: tt.jobs}
			_, err := Validate(&p)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		retry   *pm.Retry
		wantErr error
	}{
		{
			name:  "valid retry",
			retry: &pm.Retry{Attempts: 3, Delay: 5 * time.Second, Backoff: pm.BackoffExponential},
		},
		{
			name:  "nil retry is valid",
			retry: nil,
		},
		{
			name:    "zero attempts",
			retry:   &pm.Retry{Attempts: 0, Backoff: pm.BackoffNone},
			wantErr: pm.ErrInvalidRetryAttempts,
		},
		{
			name:    "negative attempts",
			retry:   &pm.Retry{Attempts: -1, Backoff: pm.BackoffNone},
			wantErr: pm.ErrInvalidRetryAttempts,
		},
		{
			name:    "invalid backoff strategy",
			retry:   &pm.Retry{Attempts: 1, Backoff: "quadratic"},
			wantErr: pm.ErrInvalidBackoff,
		},
		{
			name:    "negative delay",
			retry:   &pm.Retry{Attempts: 1, Delay: -time.Second, Backoff: pm.BackoffNone},
			wantErr: pm.ErrNegativeDelay,
		},
		{
			name:  "linear backoff valid",
			retry: &pm.Retry{Attempts: 2, Delay: time.Second, Backoff: pm.BackoffLinear},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := pm.Pipeline{
				Name: "test",
				Jobs: []pm.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pm.Step{{Name: "build", Run: []string{"echo hi"}}},
						Retry: tt.retry,
					},
				},
			}
			_, err := Validate(&p)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateStepRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		retry   *pm.Retry
		wantErr error
	}{
		{name: "nil step retry valid", retry: nil},
		{
			name:  "valid step retry",
			retry: &pm.Retry{Attempts: 2, Delay: time.Second, Backoff: pm.BackoffLinear},
		},
		{
			name:    "step retry zero attempts",
			retry:   &pm.Retry{Attempts: 0, Backoff: pm.BackoffNone},
			wantErr: pm.ErrInvalidRetryAttempts,
		},
		{
			name:    "step retry invalid backoff",
			retry:   &pm.Retry{Attempts: 1, Backoff: "bad"},
			wantErr: pm.ErrInvalidBackoff,
		},
		{
			name:    "step retry negative delay",
			retry:   &pm.Retry{Attempts: 1, Delay: -time.Second, Backoff: pm.BackoffNone},
			wantErr: pm.ErrNegativeDelay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := pm.Pipeline{
				Name: "test",
				Jobs: []pm.Job{
					{
						Name:  "build",
						Image: "alpine:latest",
						Steps: []pm.Step{{Name: "s", Run: []string{"echo"}, Retry: tt.retry}},
					},
				},
			}
			_, err := Validate(&p)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		timeout time.Duration
		wantErr error
	}{
		{
			name:    "valid timeout",
			timeout: 10 * time.Minute,
		},
		{
			name:    "zero timeout is valid",
			timeout: 0,
		},
		{
			name:    "negative timeout",
			timeout: -1 * time.Second,
			wantErr: pm.ErrNegativeTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := pm.Pipeline{
				Name: "test",
				Jobs: []pm.Job{
					{
						Name:    "build",
						Image:   "alpine:latest",
						Steps:   []pm.Step{{Name: "build", Run: []string{"echo hi"}}},
						Timeout: tt.timeout,
					},
				},
			}
			_, err := Validate(&p)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateStepTimeout(t *testing.T) {
	t.Parallel()

	p := pm.Pipeline{
		Name: "test",
		Jobs: []pm.Job{
			{
				Name:  "build",
				Image: "alpine:latest",
				Steps: []pm.Step{{
					Name:    "build",
					Run:     []string{"echo hi"},
					Timeout: -5 * time.Second,
				}},
			},
		},
	}
	_, err := Validate(&p)
	require.ErrorIs(t, err, pm.ErrNegativeTimeout)
}

func TestApplyDefaultsShell(t *testing.T) {
	t.Parallel()

	defaults := &pm.Defaults{Shell: []string{"/bin/bash", "-c"}}
	jobs := []pm.Job{
		{Name: "no-shell", Image: "alpine:latest"},
		{Name: "has-shell", Image: "alpine:latest", Shell: []string{"/bin/zsh", "-c"}},
	}

	result := ApplyDefaults(jobs, defaults)

	assert.Equal(t, []string{"/bin/bash", "-c"}, result[0].Shell, "job without shell inherits defaults")
	assert.Equal(t, []string{"/bin/zsh", "-c"}, result[1].Shell, "job with shell keeps its own")
}
