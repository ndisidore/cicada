package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

func TestExpand(t *testing.T) {
	t.Parallel()

	t.Run("Passthrough", func(t *testing.T) {
		t.Parallel()

		input := pm.Pipeline{
			Name: "ci",
			Jobs: []pm.Job{
				{Name: "build", Image: "golang:1.23", Steps: []pm.Step{{Name: "build", Run: []string{"go build"}}}},
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
			input pm.Pipeline
			want  pm.Pipeline
		}{
			{
				name: "single dim",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []pm.Job{
						{Name: "build", Image: "golang:1.23", Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=${matrix.os} go build"}}}},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "build[os=linux]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "linux"}, Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=linux go build"}}}},
						{Name: "build[os=darwin]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "darwin"}, Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=darwin go build"}}}},
					},
				},
			},
			{
				name: "correlated deps",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []pm.Job{
						{Name: "build", Image: "golang:1.23", Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=${matrix.os} go build"}}}},
						{Name: "test", Image: "golang:1.23", DependsOn: []string{"build"}, Steps: []pm.Step{{Name: "test", Run: []string{"GOOS=${matrix.os} go test"}}}},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "build[os=linux]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "linux"}, Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=linux go build"}}}},
						{Name: "test[os=linux]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "linux"}, DependsOn: []string{"build[os=linux]"}, Steps: []pm.Step{{Name: "test", Run: []string{"GOOS=linux go test"}}}},
						{Name: "build[os=darwin]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "darwin"}, Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=darwin go build"}}}},
						{Name: "test[os=darwin]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "darwin"}, DependsOn: []string{"build[os=darwin]"}, Steps: []pm.Step{{Name: "test", Run: []string{"GOOS=darwin go test"}}}},
					},
				},
			},
			{
				name: "cache ID namespacing",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []pm.Job{
						{
							Name:   "build",
							Image:  "golang:1.23",
							Caches: []pm.Cache{{ID: "gomod", Target: "/go/pkg/mod"}},
							Steps:  []pm.Step{{Name: "build", Run: []string{"go build"}}},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:         "build[os=linux]",
							Image:        "golang:1.23",
							MatrixValues: map[string]string{"os": "linux"},
							Caches:       []pm.Cache{{ID: "gomod--build[os=linux]", Target: "/go/pkg/mod"}},
							Steps:        []pm.Step{{Name: "build", Run: []string{"go build"}}},
						},
						{
							Name:         "build[os=darwin]",
							Image:        "golang:1.23",
							MatrixValues: map[string]string{"os": "darwin"},
							Caches:       []pm.Cache{{ID: "gomod--build[os=darwin]", Target: "/go/pkg/mod"}},
							Steps:        []pm.Step{{Name: "build", Run: []string{"go build"}}},
						},
					},
				},
			},
			{
				name: "step-level artifact From correlated",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []pm.Job{
						{Name: "build", Image: "golang:1.23", Steps: []pm.Step{{Name: "compile", Run: []string{"go build"}}}},
						{
							Name: "test", Image: "golang:1.23", DependsOn: []string{"build"},
							Steps: []pm.Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []pm.Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "build[os=linux]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "linux"}, Steps: []pm.Step{{Name: "compile", Run: []string{"go build"}}}},
						{
							Name: "test[os=linux]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "linux"}, DependsOn: []string{"build[os=linux]"},
							Steps: []pm.Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []pm.Artifact{{From: "build[os=linux]", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
						{Name: "build[os=darwin]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "darwin"}, Steps: []pm.Step{{Name: "compile", Run: []string{"go build"}}}},
						{
							Name: "test[os=darwin]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "darwin"}, DependsOn: []string{"build[os=darwin]"},
							Steps: []pm.Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []pm.Artifact{{From: "build[os=darwin]", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
					},
				},
			},
			{
				name: "step-level cache ID namespaced",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []pm.Job{
						{
							Name: "build", Image: "golang:1.23",
							Steps: []pm.Step{{
								Name:   "compile",
								Run:    []string{"go build"},
								Caches: []pm.Cache{{ID: "gomod", Target: "/go/pkg/mod"}},
							}},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name: "build[os=linux]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "linux"},
							Steps: []pm.Step{{
								Name:   "compile",
								Run:    []string{"go build"},
								Caches: []pm.Cache{{ID: "gomod--build[os=linux]", Target: "/go/pkg/mod"}},
							}},
						},
						{
							Name: "build[os=darwin]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "darwin"},
							Steps: []pm.Step{{
								Name:   "compile",
								Run:    []string{"go build"},
								Caches: []pm.Cache{{ID: "gomod--build[os=darwin]", Target: "/go/pkg/mod"}},
							}},
						},
					},
				},
			},
			{
				name: "platform substitution",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "platform", Values: []string{"linux/amd64", "linux/arm64"}},
						},
					},
					Jobs: []pm.Job{
						{Name: "build", Image: "golang:1.23", Platform: "${matrix.platform}", Steps: []pm.Step{{Name: "build", Run: []string{"go version"}}}},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "build[platform=linux/amd64]", Image: "golang:1.23", Platform: "linux/amd64", MatrixValues: map[string]string{"platform": "linux/amd64"}, Steps: []pm.Step{{Name: "build", Run: []string{"go version"}}}},
						{Name: "build[platform=linux/arm64]", Image: "golang:1.23", Platform: "linux/arm64", MatrixValues: map[string]string{"platform": "linux/arm64"}, Steps: []pm.Step{{Name: "build", Run: []string{"go version"}}}},
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
			input pm.Pipeline
			want  pm.Pipeline
		}{
			{
				name: "single dim",
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:  "test",
							Image: "golang:${matrix.go-version}",
							Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "go-version", Values: []string{"1.21", "1.22", "1.23"}},
								},
							},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "test[go-version=1.21]", Image: "golang:1.21", MatrixValues: map[string]string{"go-version": "1.21"}, Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "test[go-version=1.22]", Image: "golang:1.22", MatrixValues: map[string]string{"go-version": "1.22"}, Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "test[go-version=1.23]", Image: "golang:1.23", MatrixValues: map[string]string{"go-version": "1.23"}, Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}}},
					},
				},
			},
			{
				name: "all-variant deps",
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:  "test",
							Image: "golang:${matrix.go-version}",
							Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "go-version", Values: []string{"1.21", "1.22"}},
								},
							},
						},
						{
							Name:      "deploy",
							Image:     "alpine:latest",
							DependsOn: []string{"test"},
							Steps:     []pm.Step{{Name: "deploy", Run: []string{"echo deploy"}}},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "test[go-version=1.21]", Image: "golang:1.21", MatrixValues: map[string]string{"go-version": "1.21"}, Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "test[go-version=1.22]", Image: "golang:1.22", MatrixValues: map[string]string{"go-version": "1.22"}, Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}}},
						{Name: "deploy", Image: "alpine:latest", DependsOn: []string{"test[go-version=1.21]", "test[go-version=1.22]"}, Steps: []pm.Step{{Name: "deploy", Run: []string{"echo deploy"}}}},
					},
				},
			},
			{
				name: "substitution in all fields",
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:    "build",
							Image:   "golang:${matrix.go}",
							Workdir: "/src/${matrix.go}",
							Mounts:  []pm.Mount{{Source: "./${matrix.go}", Target: "/mnt/${matrix.go}"}},
							Caches:  []pm.Cache{{ID: "cache", Target: "/cache/${matrix.go}"}},
							Steps:   []pm.Step{{Name: "build", Run: []string{"GOARCH=${matrix.go} build"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "go", Values: []string{"1.21"}},
								},
							},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:         "build[go=1.21]",
							Image:        "golang:1.21",
							MatrixValues: map[string]string{"go": "1.21"},
							Workdir:      "/src/1.21",
							Mounts:       []pm.Mount{{Source: "./1.21", Target: "/mnt/1.21"}},
							Caches:       []pm.Cache{{ID: "cache--build[go=1.21]", Target: "/cache/1.21"}},
							Steps:        []pm.Step{{Name: "build", Run: []string{"GOARCH=1.21 build"}}},
						},
					},
				},
			},
			{
				name: "cache ID with matrix var",
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:   "build",
							Image:  "golang:1.23",
							Caches: []pm.Cache{{ID: "gomod-${matrix.os}", Target: "/go/pkg/mod"}},
							Steps:  []pm.Step{{Name: "build", Run: []string{"go build"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "os", Values: []string{"linux", "darwin"}},
								},
							},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:         "build[os=linux]",
							Image:        "golang:1.23",
							MatrixValues: map[string]string{"os": "linux"},
							Caches:       []pm.Cache{{ID: "gomod-linux--build[os=linux]", Target: "/go/pkg/mod"}},
							Steps:        []pm.Step{{Name: "build", Run: []string{"go build"}}},
						},
						{
							Name:         "build[os=darwin]",
							Image:        "golang:1.23",
							MatrixValues: map[string]string{"os": "darwin"},
							Caches:       []pm.Cache{{ID: "gomod-darwin--build[os=darwin]", Target: "/go/pkg/mod"}},
							Steps:        []pm.Step{{Name: "build", Run: []string{"go build"}}},
						},
					},
				},
			},
			{
				name: "step-level artifact From rewritten to single variant",
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name: "build", Image: "golang:1.23",
							Steps: []pm.Step{{Name: "compile", Run: []string{"go build"}}},
						},
						{
							Name: "test", Image: "golang:${matrix.v}", DependsOn: []string{"build"},
							Steps: []pm.Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []pm.Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "v", Values: []string{"1.21", "1.22"}},
								},
							},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name: "build", Image: "golang:1.23",
							Steps: []pm.Step{{Name: "compile", Run: []string{"go build"}}},
						},
						{
							Name: "test[v=1.21]", Image: "golang:1.21", MatrixValues: map[string]string{"v": "1.21"}, DependsOn: []string{"build"},
							Steps: []pm.Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []pm.Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
							}},
						},
						{
							Name: "test[v=1.22]", Image: "golang:1.22", MatrixValues: map[string]string{"v": "1.22"}, DependsOn: []string{"build"},
							Steps: []pm.Step{{
								Name:      "test",
								Run:       []string{"go test"},
								Artifacts: []pm.Artifact{{From: "build", Source: "/out/bin", Target: "/usr/local/bin"}},
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
			input pm.Pipeline
			want  pm.Pipeline
		}{
			{
				name: "pipeline and job matrix",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []pm.Job{
						{Name: "build", Image: "golang:1.23", Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=${matrix.os} go build"}}}},
						{
							Name:      "test",
							Image:     "golang:${matrix.go-version}",
							DependsOn: []string{"build"},
							Steps:     []pm.Step{{Name: "test", Run: []string{"GOOS=${matrix.os} go test"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "go-version", Values: []string{"1.21", "1.22"}},
								},
							},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "build[os=linux]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "linux"}, Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=linux go build"}}}},
						{Name: "test[go-version=1.21,os=linux]", Image: "golang:1.21", MatrixValues: map[string]string{"os": "linux", "go-version": "1.21"}, DependsOn: []string{"build[os=linux]"}, Steps: []pm.Step{{Name: "test", Run: []string{"GOOS=linux go test"}}}},
						{Name: "test[go-version=1.22,os=linux]", Image: "golang:1.22", MatrixValues: map[string]string{"os": "linux", "go-version": "1.22"}, DependsOn: []string{"build[os=linux]"}, Steps: []pm.Step{{Name: "test", Run: []string{"GOOS=linux go test"}}}},
						{Name: "build[os=darwin]", Image: "golang:1.23", MatrixValues: map[string]string{"os": "darwin"}, Steps: []pm.Step{{Name: "build", Run: []string{"GOOS=darwin go build"}}}},
						{Name: "test[go-version=1.21,os=darwin]", Image: "golang:1.21", MatrixValues: map[string]string{"os": "darwin", "go-version": "1.21"}, DependsOn: []string{"build[os=darwin]"}, Steps: []pm.Step{{Name: "test", Run: []string{"GOOS=darwin go test"}}}},
						{Name: "test[go-version=1.22,os=darwin]", Image: "golang:1.22", MatrixValues: map[string]string{"os": "darwin", "go-version": "1.22"}, DependsOn: []string{"build[os=darwin]"}, Steps: []pm.Step{{Name: "test", Run: []string{"GOOS=darwin go test"}}}},
					},
				},
			},
			{
				name: "cache ID double-namespaced",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux"}},
						},
					},
					Jobs: []pm.Job{
						{
							Name:   "test",
							Image:  "golang:${matrix.go-version}",
							Caches: []pm.Cache{{ID: "gomod", Target: "/go/pkg/mod"}},
							Steps:  []pm.Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "go-version", Values: []string{"1.21"}},
								},
							},
						},
					},
				},
				want: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:         "test[go-version=1.21,os=linux]",
							Image:        "golang:1.21",
							MatrixValues: map[string]string{"os": "linux", "go-version": "1.21"},
							Caches:       []pm.Cache{{ID: "gomod--test[os=linux]--test[go-version=1.21,os=linux]", Target: "/go/pkg/mod"}},
							Steps:        []pm.Step{{Name: "test", Run: []string{"go test"}}},
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
			input   pm.Pipeline
			wantErr error
		}{
			{
				name:    "dimension name collision between pipeline and job",
				wantErr: pm.ErrDuplicateDim,
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux"}},
						},
					},
					Jobs: []pm.Job{
						{
							Name: "test", Image: "alpine",
							Steps: []pm.Step{{Name: "test", Run: []string{"echo"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "os", Values: []string{"darwin"}},
								},
							},
						},
					},
				},
			},
			{
				name:    "empty pipeline matrix",
				wantErr: pm.ErrEmptyMatrix,
				input: pm.Pipeline{
					Name:   "ci",
					Matrix: &pm.Matrix{},
					Jobs: []pm.Job{
						{Name: "a", Image: "alpine", Steps: []pm.Step{{Name: "a", Run: []string{"echo"}}}},
					},
				},
			},
			{
				name:    "empty job matrix",
				wantErr: pm.ErrEmptyMatrix,
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "a", Image: "alpine", Steps: []pm.Step{{Name: "a", Run: []string{"echo"}}}, Matrix: &pm.Matrix{}},
					},
				},
			},
			{
				name:    "empty dimension values",
				wantErr: pm.ErrEmptyDimension,
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name: "a", Image: "alpine",
							Steps: []pm.Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{{Name: "os", Values: []string{}}},
							},
						},
					},
				},
			},
			{
				name:    "invalid dimension name",
				wantErr: pm.ErrInvalidDimName,
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name: "a", Image: "alpine",
							Steps: []pm.Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{{Name: "my os!", Values: []string{"linux"}}},
							},
						},
					},
				},
			},
			{
				name:    "exceeds combination limit",
				wantErr: pm.ErrMatrixTooLarge,
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name: "a", Image: "alpine",
							Steps: []pm.Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
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
				wantErr: pm.ErrDuplicateDim,
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name: "a", Image: "alpine",
							Steps: []pm.Step{{Name: "a", Run: []string{"echo"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
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

	t.Run("MatrixValues", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			input      pm.Pipeline
			wantLen    int
			wantValues []map[string]string // nil element asserts nil MatrixValues
		}{
			{
				name: "pipeline matrix sets MatrixValues",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux", "darwin"}},
						},
					},
					Jobs: []pm.Job{
						{Name: "build", Image: "golang:1.23", Steps: []pm.Step{{Name: "build", Run: []string{"go build"}}}},
					},
				},
				wantLen:    2,
				wantValues: []map[string]string{{"os": "linux"}, {"os": "darwin"}},
			},
			{
				name: "job matrix sets MatrixValues",
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{
							Name:  "test",
							Image: "golang:${matrix.v}",
							Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "v", Values: []string{"1.24", "1.25"}},
								},
							},
						},
					},
				},
				wantLen:    2,
				wantValues: []map[string]string{{"v": "1.24"}, {"v": "1.25"}},
			},
			{
				name: "combined pipeline and job matrix merges MatrixValues",
				input: pm.Pipeline{
					Name: "ci",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "os", Values: []string{"linux"}},
						},
					},
					Jobs: []pm.Job{
						{
							Name:  "test",
							Image: "golang:${matrix.v}",
							Steps: []pm.Step{{Name: "test", Run: []string{"go test"}}},
							Matrix: &pm.Matrix{
								Dimensions: []pm.Dimension{
									{Name: "v", Values: []string{"1.25"}},
								},
							},
						},
					},
				},
				wantLen:    1,
				wantValues: []map[string]string{{"os": "linux", "v": "1.25"}},
			},
			{
				name: "non-matrix job has nil MatrixValues",
				input: pm.Pipeline{
					Name: "ci",
					Jobs: []pm.Job{
						{Name: "build", Image: "golang:1.23", Steps: []pm.Step{{Name: "build", Run: []string{"go build"}}}},
					},
				},
				wantLen:    1,
				wantValues: []map[string]string{nil},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				got, err := Expand(tt.input)
				require.NoError(t, err)
				require.Len(t, got.Jobs, tt.wantLen)
				for i, wv := range tt.wantValues {
					if wv == nil {
						assert.Nil(t, got.Jobs[i].MatrixValues)
					} else {
						assert.Equal(t, wv, got.Jobs[i].MatrixValues)
					}
				}
			})
		}
	})

	t.Run("InputImmutability", func(t *testing.T) {
		t.Parallel()

		input := pm.Pipeline{
			Name: "ci",
			Jobs: []pm.Job{
				{
					Name:  "build",
					Image: "golang:1.23",
					Steps: []pm.Step{{Name: "build", Run: []string{"go build"}}},
				},
				{
					Name:  "test",
					Image: "golang:${matrix.version}",
					Matrix: &pm.Matrix{Dimensions: []pm.Dimension{
						{Name: "version", Values: []string{"1.22", "1.23"}},
					}},
					DependsOn: []string{"build"},
					Artifacts: []pm.Artifact{{From: "build", Source: "/out", Target: "/in"}},
					Steps:     []pm.Step{{Name: "test", Run: []string{"go test"}}},
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
