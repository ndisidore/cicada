package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatrixCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		m       Matrix
		want    []map[string]string
		wantErr error
	}{
		{
			name: "single dimension",
			m: Matrix{
				Dimensions: []Dimension{
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
			m: Matrix{
				Dimensions: []Dimension{
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
			m: Matrix{
				Dimensions: []Dimension{
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
			m:    Matrix{},
			want: nil,
		},
		{
			name: "single value dimension",
			m: Matrix{
				Dimensions: []Dimension{
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
		p    Pipeline
		want []string
	}{
		{
			name: "unique images",
			p: Pipeline{
				Steps: []Step{
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
					{Image: "rust:1.76"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23", "rust:1.76"},
		},
		{
			name: "deduplicates",
			p: Pipeline{
				Steps: []Step{
					{Image: "alpine:latest"},
					{Image: "alpine:latest"},
					{Image: "golang:1.23"},
				},
			},
			want: []string{"alpine:latest", "golang:1.23"},
		},
		{
			name: "empty pipeline",
			p:    Pipeline{},
			want: nil,
		},
		{
			name: "single step",
			p: Pipeline{
				Steps: []Step{
					{Image: "ubuntu:22.04"},
				},
			},
			want: []string{"ubuntu:22.04"},
		},
		{
			name: "skips empty image refs",
			p: Pipeline{
				Steps: []Step{
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
		env     []EnvVar
		wantErr error
	}{
		{
			name: "valid env vars pass",
			env: []EnvVar{
				{Key: "FOO", Value: "bar"},
				{Key: "BAZ", Value: "qux"},
			},
		},
		{
			name: "empty env key returns ErrEmptyEnvKey",
			env: []EnvVar{
				{Key: "", Value: "bar"},
			},
			wantErr: ErrEmptyEnvKey,
		},
		{
			name: "duplicate env key returns ErrDuplicateEnvKey",
			env: []EnvVar{
				{Key: "FOO", Value: "bar"},
				{Key: "FOO", Value: "baz"},
			},
			wantErr: ErrDuplicateEnvKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			p := Pipeline{
				Name: "test-pipeline",
				Steps: []Step{
					{
						Name:  "build",
						Image: "golang:1.23",
						Run:   []string{"go build ./..."},
						Env:   tt.env,
					},
				},
			}

			// Act
			_, err := p.Validate()

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
		env     []EnvVar
		wantErr error
	}{
		{
			name: "valid pipeline env vars pass",
			env: []EnvVar{
				{Key: "CI", Value: "true"},
				{Key: "REGION", Value: "us-east-1"},
			},
		},
		{
			name:    "empty pipeline env key returns ErrEmptyEnvKey",
			env:     []EnvVar{{Key: "", Value: "bar"}},
			wantErr: ErrEmptyEnvKey,
		},
		{
			name: "duplicate pipeline env key returns ErrDuplicateEnvKey",
			env: []EnvVar{
				{Key: "CI", Value: "true"},
				{Key: "CI", Value: "false"},
			},
			wantErr: ErrDuplicateEnvKey,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := Pipeline{
				Name: "test-pipeline",
				Env:  tt.env,
				Steps: []Step{
					{
						Name:  "build",
						Image: "alpine:latest",
						Run:   []string{"echo hi"},
					},
				},
			}

			_, err := p.Validate()

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
		exports []Export
		wantErr error
	}{
		{
			name: "valid file export passes",
			exports: []Export{
				{Path: "/output/build.tar", Local: "./build.tar"},
			},
		},
		{
			name: "valid directory export with trailing slash passes",
			exports: []Export{
				{Path: "/output/dist/", Local: "./dist"},
			},
		},
		{
			name: "empty export path returns ErrEmptyExportPath",
			exports: []Export{
				{Path: ""},
			},
			wantErr: ErrEmptyExportPath,
		},
		{
			name: "relative export path returns ErrRelativeExport",
			exports: []Export{
				{Path: "output/build.tar"},
			},
			wantErr: ErrRelativeExport,
		},
		{
			name: "root export path returns ErrRootExport",
			exports: []Export{
				{Path: "/"},
			},
			wantErr: ErrRootExport,
		},
		{
			name: "root with multiple slashes returns ErrRootExport",
			exports: []Export{
				{Path: "///"},
			},
			wantErr: ErrRootExport,
		},
		{
			name: "empty export local returns ErrEmptyExportLocal",
			exports: []Export{
				{Path: "/output/build.tar", Local: ""},
			},
			wantErr: ErrEmptyExportLocal,
		},
		{
			name: "duplicate export path returns ErrDuplicateExport",
			exports: []Export{
				{Path: "/output/build.tar", Local: "./build.tar"},
				{Path: "/output/build.tar", Local: "./build2.tar"},
			},
			wantErr: ErrDuplicateExport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			p := Pipeline{
				Name: "test-pipeline",
				Steps: []Step{
					{
						Name:    "build",
						Image:   "golang:1.23",
						Run:     []string{"go build ./..."},
						Exports: tt.exports,
					},
				},
			}

			// Act
			_, err := p.Validate()

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
		steps   []Step
		wantErr error
	}{
		{
			name: "valid artifact passes",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build -o /out/app ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "/out/app", Target: "/app/bin"},
					},
				},
			},
		},
		{
			name: "empty artifact From returns ErrEmptyArtifactFrom",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "", Source: "/out/app", Target: "/app/bin"},
					},
				},
			},
			wantErr: ErrEmptyArtifactFrom,
		},
		{
			name: "empty artifact Source returns ErrEmptyArtifactSource",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "", Target: "/app/bin"},
					},
				},
			},
			wantErr: ErrEmptyArtifactSource,
		},
		{
			name: "empty artifact Target returns ErrEmptyArtifactTarget",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "/out/app", Target: ""},
					},
				},
			},
			wantErr: ErrEmptyArtifactTarget,
		},
		{
			name: "artifact From not in DependsOn returns ErrArtifactNoDep",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:  "deploy",
					Image: "alpine:latest",
					Run:   []string{"./deploy.sh"},
					Artifacts: []Artifact{
						{From: "compile", Source: "/out/app", Target: "/app/bin"},
					},
				},
			},
			wantErr: ErrArtifactNoDep,
		},
		{
			name: "relative artifact source returns ErrRelativeArtifactSource",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "out/app", Target: "/app/bin"},
					},
				},
			},
			wantErr: ErrRelativeArtifactSource,
		},
		{
			name: "relative artifact target returns ErrRelativeArtifact",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "/out/app", Target: "app/bin"},
					},
				},
			},
			wantErr: ErrRelativeArtifact,
		},
		{
			name: "root artifact target returns ErrRootArtifact",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "/out/app", Target: "/"},
					},
				},
			},
			wantErr: ErrRootArtifact,
		},
		{
			name: "root with slashes artifact target returns ErrRootArtifact",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "/out/app", Target: "///"},
					},
				},
			},
			wantErr: ErrRootArtifact,
		},
		{
			name: "duplicate artifact target returns ErrDuplicateArtifact",
			steps: []Step{
				{
					Name:  "compile",
					Image: "golang:1.23",
					Run:   []string{"go build ./..."},
				},
				{
					Name:      "deploy",
					Image:     "alpine:latest",
					Run:       []string{"./deploy.sh"},
					DependsOn: []string{"compile"},
					Artifacts: []Artifact{
						{From: "compile", Source: "/out/app", Target: "/app/bin"},
						{From: "compile", Source: "/out/lib", Target: "/app/bin"},
					},
				},
			},
			wantErr: ErrDuplicateArtifact,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Arrange
			p := Pipeline{
				Name:  "test-pipeline",
				Steps: tt.steps,
			}

			// Act
			_, err := p.Validate()

			// Assert
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}
