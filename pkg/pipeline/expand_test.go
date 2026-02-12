package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpand(t *testing.T) {
	t.Parallel()

	t.Run("Passthrough", func(t *testing.T) {
		t.Parallel()

		input := Pipeline{
			Name: "ci",
			Jobs: []Job{
				{Name: "build", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"go build"}}}},
			},
		}
		got, err := Expand(input)
		require.NoError(t, err)
		assert.Equal(t, input, got)
	})

	t.Run("PipelineMatrix", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name  string
			input Pipeline
			want  Pipeline
		}{
			{
				name: "single dim",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []Job{
						{Name: "build", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=${matrix.os} go build"}}}},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "build[os=linux]", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=linux go build"}}}},
						{Name: "build[os=darwin]", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=darwin go build"}}}},
					},
				},
			},
			{
				name: "correlated deps",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []Job{
						{Name: "build", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=${matrix.os} go build"}}}},
						{Name: "test", Image: "golang:1.23", DependsOn: []string{"build"}, Steps: []Step{{Name: "test", Run: []string{"GOOS=${matrix.os} go test"}}}},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "build[os=linux]", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=linux go build"}}}},
						{Name: "test[os=linux]", Image: "golang:1.23", DependsOn: []string{"build[os=linux]"}, Steps: []Step{{Name: "test", Run: []string{"GOOS=linux go test"}}}},
						{Name: "build[os=darwin]", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=darwin go build"}}}},
						{Name: "test[os=darwin]", Image: "golang:1.23", DependsOn: []string{"build[os=darwin]"}, Steps: []Step{{Name: "test", Run: []string{"GOOS=darwin go test"}}}},
					},
				},
			},
			{
				name: "cache ID namespacing",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []Job{
						{
							Name:   "build",
							Image:  "golang:1.23",
							Caches: []Cache{{ID: "gomod", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "build", Run: []string{"go build"}}},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:   "build[os=linux]",
							Image:  "golang:1.23",
							Caches: []Cache{{ID: "gomod--build[os=linux]", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "build", Run: []string{"go build"}}},
						},
						{
							Name:   "build[os=darwin]",
							Image:  "golang:1.23",
							Caches: []Cache{{ID: "gomod--build[os=darwin]", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "build", Run: []string{"go build"}}},
						},
					},
				},
			},
			{
				name: "step-level artifact From correlated",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []Job{
						{Name: "build", Image: "golang:1.23", Steps: []Step{{Name: "compile", Run: []string{"go build"}}}},
						{
							Name: "test", Image: "golang:1.23", DependsOn: []string{"build"},
							Steps: []Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "build[os=linux]", Image: "golang:1.23", Steps: []Step{{Name: "compile", Run: []string{"go build"}}}},
						{
							Name: "test[os=linux]", Image: "golang:1.23", DependsOn: []string{"build[os=linux]"},
							Steps: []Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []Artifact{{From: "build[os=linux]", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
						{Name: "build[os=darwin]", Image: "golang:1.23", Steps: []Step{{Name: "compile", Run: []string{"go build"}}}},
						{
							Name: "test[os=darwin]", Image: "golang:1.23", DependsOn: []string{"build[os=darwin]"},
							Steps: []Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []Artifact{{From: "build[os=darwin]", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
					},
				},
			},
			{
				name: "step-level cache ID namespaced",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []Job{
						{
							Name: "build", Image: "golang:1.23",
							Steps: []Step{{
								Name:   "compile",
								Run:    []string{"go build"},
								Caches: []Cache{{ID: "gomod", Target: "/go/pkg/mod"}},
							}},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name: "build[os=linux]", Image: "golang:1.23",
							Steps: []Step{{
								Name:   "compile",
								Run:    []string{"go build"},
								Caches: []Cache{{ID: "gomod--build[os=linux]", Target: "/go/pkg/mod"}},
							}},
						},
						{
							Name: "build[os=darwin]", Image: "golang:1.23",
							Steps: []Step{{
								Name:   "compile",
								Run:    []string{"go build"},
								Caches: []Cache{{ID: "gomod--build[os=darwin]", Target: "/go/pkg/mod"}},
							}},
						},
					},
				},
			},
			{
				name: "platform substitution",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "platform", Values: []string{"linux/amd64", "linux/arm64"}},
						},
					},
					Jobs: []Job{
						{Name: "build", Image: "golang:1.23", Platform: "${matrix.platform}", Steps: []Step{{Name: "build", Run: []string{"go version"}}}},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "build[platform=linux/amd64]", Image: "golang:1.23", Platform: "linux/amd64", Steps: []Step{{Name: "build", Run: []string{"go version"}}}},
						{Name: "build[platform=linux/arm64]", Image: "golang:1.23", Platform: "linux/arm64", Steps: []Step{{Name: "build", Run: []string{"go version"}}}},
					},
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				got, err := Expand(tt.input)
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("JobMatrix", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name  string
			input Pipeline
			want  Pipeline
		}{
			{
				name: "single dim",
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:  "test",
							Image: "golang:${matrix.go-version}",
							Steps: []Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "go-version", Values: []string{"1.21", "1.22", "1.23"}},
								},
							},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "test[go-version=1.21]", Image: "golang:1.21", Steps: []Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "test[go-version=1.22]", Image: "golang:1.22", Steps: []Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "test[go-version=1.23]", Image: "golang:1.23", Steps: []Step{{Name: "test", Run: []string{"go test"}}}},
					},
				},
			},
			{
				name: "all-variant deps",
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:  "test",
							Image: "golang:${matrix.go-version}",
							Steps: []Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "go-version", Values: []string{"1.21", "1.22"}},
								},
							},
						},
						{
							Name:      "deploy",
							Image:     "alpine:latest",
							DependsOn: []string{"test"},
							Steps:     []Step{{Name: "deploy", Run: []string{"echo deploy"}}},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "test[go-version=1.21]", Image: "golang:1.21", Steps: []Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "test[go-version=1.22]", Image: "golang:1.22", Steps: []Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "deploy", Image: "alpine:latest", DependsOn: []string{"test[go-version=1.21]", "test[go-version=1.22]"}, Steps: []Step{{Name: "deploy", Run: []string{"echo deploy"}}}},
					},
				},
			},
			{
				name: "substitution in all fields",
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:    "build",
							Image:   "golang:${matrix.go}",
							Workdir: "/src/${matrix.go}",
							Mounts:  []Mount{{Source: "./${matrix.go}", Target: "/mnt/${matrix.go}"}},
							Caches:  []Cache{{ID: "cache", Target: "/cache/${matrix.go}"}},
							Steps:   []Step{{Name: "build", Run: []string{"GOARCH=${matrix.go} build"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "go", Values: []string{"1.21"}},
								},
							},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:    "build[go=1.21]",
							Image:   "golang:1.21",
							Workdir: "/src/1.21",
							Mounts:  []Mount{{Source: "./1.21", Target: "/mnt/1.21"}},
							Caches:  []Cache{{ID: "cache--build[go=1.21]", Target: "/cache/1.21"}},
							Steps:   []Step{{Name: "build", Run: []string{"GOARCH=1.21 build"}}},
						},
					},
				},
			},
			{
				name: "cache ID with matrix var",
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:   "build",
							Image:  "golang:1.23",
							Caches: []Cache{{ID: "gomod-${matrix.os}", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "build", Run: []string{"go build"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "os", Values: []string{"linux", "darwin"}},
								},
							},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:   "build[os=linux]",
							Image:  "golang:1.23",
							Caches: []Cache{{ID: "gomod-linux--build[os=linux]", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "build", Run: []string{"go build"}}},
						},
						{
							Name:   "build[os=darwin]",
							Image:  "golang:1.23",
							Caches: []Cache{{ID: "gomod-darwin--build[os=darwin]", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "build", Run: []string{"go build"}}},
						},
					},
				},
			},
			{
				name: "step-level artifact From rewritten to single variant",
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name: "build", Image: "golang:1.23",
							Steps: []Step{{Name: "compile", Run: []string{"go build"}}},
						},
						{
							Name: "test", Image: "golang:${matrix.v}", DependsOn: []string{"build"},
							Steps: []Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "v", Values: []string{"1.21", "1.22"}},
								},
							},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name: "build", Image: "golang:1.23",
							Steps: []Step{{Name: "compile", Run: []string{"go build"}}},
						},
						{
							Name: "test[v=1.21]", Image: "golang:1.21", DependsOn: []string{"build"},
							Steps: []Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
						{
							Name: "test[v=1.22]", Image: "golang:1.22", DependsOn: []string{"build"},
							Steps: []Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
					},
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				got, err := Expand(tt.input)
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("Combined", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name  string
			input Pipeline
			want  Pipeline
		}{
			{
				name: "pipeline and job matrix",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []Job{
						{Name: "build", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=${matrix.os} go build"}}}},
						{
							Name:      "test",
							Image:     "golang:${matrix.go-version}",
							DependsOn: []string{"build"},
							Steps:     []Step{{Name: "test", Run: []string{"GOOS=${matrix.os} go test"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "go-version", Values: []string{"1.21", "1.22"}},
								},
							},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "build[os=linux]", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=linux go build"}}}},
						{Name: "test[go-version=1.21,os=linux]", Image: "golang:1.21", DependsOn: []string{"build[os=linux]"}, Steps: []Step{{Name: "test", Run: []string{"GOOS=linux go test"}}}},
						{Name: "test[go-version=1.22,os=linux]", Image: "golang:1.22", DependsOn: []string{"build[os=linux]"}, Steps: []Step{{Name: "test", Run: []string{"GOOS=linux go test"}}}},
						{Name: "build[os=darwin]", Image: "golang:1.23", Steps: []Step{{Name: "build", Run: []string{"GOOS=darwin go build"}}}},
						{Name: "test[go-version=1.21,os=darwin]", Image: "golang:1.21", DependsOn: []string{"build[os=darwin]"}, Steps: []Step{{Name: "test", Run: []string{"GOOS=darwin go test"}}}},
						{Name: "test[go-version=1.22,os=darwin]", Image: "golang:1.22", DependsOn: []string{"build[os=darwin]"}, Steps: []Step{{Name: "test", Run: []string{"GOOS=darwin go test"}}}},
					},
				},
			},
			{
				name: "cache ID double-namespaced",
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux"}},
						},
					},
					Jobs: []Job{
						{
							Name:   "test",
							Image:  "golang:${matrix.go-version}",
							Caches: []Cache{{ID: "gomod", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "go-version", Values: []string{"1.21"}},
								},
							},
						},
					},
				},
				want: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name:   "test[go-version=1.21,os=linux]",
							Image:  "golang:1.21",
							Caches: []Cache{{ID: "gomod--test[os=linux]--test[go-version=1.21,os=linux]", Target: "/go/pkg/mod"}},
							Steps:  []Step{{Name: "test", Run: []string{"go test"}}},
						},
					},
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				got, err := Expand(tt.input)
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			})
		}
	})

	t.Run("Validation", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name    string
			input   Pipeline
			wantErr error
		}{
			{
				name:    "dimension name collision between pipeline and job",
				wantErr: ErrDuplicateDim,
				input: Pipeline{
					Name: "ci",
					Matrix: &Matrix{
						Dimensions: []Dimension{
							{Name: "os", Values: []string{"linux"}},
						},
					},
					Jobs: []Job{
						{
							Name: "test", Image: "alpine",
							Steps: []Step{{Name: "test", Run: []string{"echo"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "os", Values: []string{"darwin"}},
								},
							},
						},
					},
				},
			},
			{
				name:    "empty pipeline matrix",
				wantErr: ErrEmptyMatrix,
				input: Pipeline{
					Name:   "ci",
					Matrix: &Matrix{},
					Jobs: []Job{
						{Name: "a", Image: "alpine", Steps: []Step{{Name: "a", Run: []string{"echo"}}}},
					},
				},
			},
			{
				name:    "empty job matrix",
				wantErr: ErrEmptyMatrix,
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{Name: "a", Image: "alpine", Steps: []Step{{Name: "a", Run: []string{"echo"}}}, Matrix: &Matrix{}},
					},
				},
			},
			{
				name:    "empty dimension values",
				wantErr: ErrEmptyDimension,
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name: "a", Image: "alpine",
							Steps: []Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{{Name: "os", Values: []string{}}},
							},
						},
					},
				},
			},
			{
				name:    "invalid dimension name",
				wantErr: ErrInvalidDimName,
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name: "a", Image: "alpine",
							Steps: []Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{{Name: "my os!", Values: []string{"linux"}}},
							},
						},
					},
				},
			},
			{
				name:    "exceeds combination limit",
				wantErr: ErrMatrixTooLarge,
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name: "a", Image: "alpine",
							Steps: []Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "a", Values: make([]string, 101)},
									{Name: "b", Values: make([]string, 101)},
								},
							},
						},
					},
				},
			},
			{
				name:    "duplicate dimension within matrix",
				wantErr: ErrDuplicateDim,
				input: Pipeline{
					Name: "ci",
					Jobs: []Job{
						{
							Name: "a", Image: "alpine",
							Steps: []Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &Matrix{
								Dimensions: []Dimension{
									{Name: "os", Values: []string{"linux"}},
									{Name: "os", Values: []string{"darwin"}},
								},
							},
						},
					},
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				_, err := Expand(tt.input)
				require.ErrorIs(t, err, tt.wantErr)
			})
		}
	})

	t.Run("InputImmutability", func(t *testing.T) {
		t.Parallel()

		input := Pipeline{
			Name: "ci",
			Jobs: []Job{
				{
					Name:  "build",
					Image: "golang:1.23",
					Steps: []Step{{Name: "build", Run: []string{"go build"}}},
				},
				{
					Name:  "test",
					Image: "golang:${matrix.version}",
					Matrix: &Matrix{Dimensions: []Dimension{
						{Name: "version", Values: []string{"1.22", "1.23"}},
					}},
					DependsOn: []string{"build"},
					Artifacts: []Artifact{{From: "build", Source: "/out", Target: "/in"}},
					Steps:     []Step{{Name: "test", Run: []string{"go test"}}},
				},
			},
		}

		origFrom := input.Jobs[1].Artifacts[0].From
		origBuildArtifacts := len(input.Jobs[0].Artifacts)

		got, err := Expand(input)
		require.NoError(t, err)
		assert.Greater(t, len(got.Jobs), len(input.Jobs), "expansion should produce more jobs")
		assert.Equal(t, origFrom, input.Jobs[1].Artifacts[0].From,
			"input artifact From must not be mutated")
		assert.Len(t, input.Jobs[0].Artifacts, origBuildArtifacts,
			"input job artifacts must not be mutated")
	})
}

func TestExpandedName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		base  string
		combo map[string]string
		want  string
	}{
		{
			name:  "single dim",
			base:  "build",
			combo: map[string]string{"os": "linux"},
			want:  "build[os=linux]",
		},
		{
			name:  "multi dim sorted",
			base:  "build",
			combo: map[string]string{"os": "linux", "arch": "amd64"},
			want:  "build[arch=amd64,os=linux]",
		},
		{
			name:  "merge with existing suffix",
			base:  "test[os=linux]",
			combo: map[string]string{"go-version": "1.22"},
			want:  "test[go-version=1.22,os=linux]",
		},
		{
			name:  "unmatched open bracket treated as no suffix",
			base:  "build[oops",
			combo: map[string]string{"os": "linux"},
			want:  "build[oops[os=linux]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := expandedName(tt.base, tt.combo)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSubstituteVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		combo map[string]string
		want  string
	}{
		{
			name:  "single replacement",
			input: "GOOS=${matrix.os} go build",
			combo: map[string]string{"os": "linux"},
			want:  "GOOS=linux go build",
		},
		{
			name:  "multiple replacements",
			input: "GOOS=${matrix.os} GOARCH=${matrix.arch}",
			combo: map[string]string{"os": "linux", "arch": "amd64"},
			want:  "GOOS=linux GOARCH=amd64",
		},
		{
			name:  "no placeholders",
			input: "go build ./...",
			combo: map[string]string{"os": "linux"},
			want:  "go build ./...",
		},
		{
			name:  "value containing placeholder is not chain-substituted",
			input: "echo ${matrix.a}",
			combo: map[string]string{"a": "${matrix.b}", "b": "WRONG"},
			want:  "echo ${matrix.b}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := substituteVars(tt.input, tt.combo, "matrix.")
			assert.Equal(t, tt.want, got)
		})
	}
}
