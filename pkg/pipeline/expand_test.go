package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   Pipeline
		want    Pipeline
		wantErr error
	}{
		{
			name: "no matrix passthrough",
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{Name: "build", Image: "golang:1.23", Run: []string{"go build"}},
				},
			},
			want: Pipeline{
				Name: "ci",
				Steps: []Step{
					{Name: "build", Image: "golang:1.23", Run: []string{"go build"}},
				},
			},
		},
		{
			name: "pipeline-level single dim",
			input: Pipeline{
				Name: "ci",
				Matrix: &Matrix{
					Dimensions: []Dimension{
						{Name: "os", Values: []string{"linux", "darwin"}},
					},
				},
				Steps: []Step{
					{Name: "build", Image: "golang:1.23", Run: []string{"GOOS=${matrix.os} go build"}},
				},
			},
			want: Pipeline{
				Name: "ci",
				Steps: []Step{
					{Name: "build[os=linux]", Image: "golang:1.23", Run: []string{"GOOS=linux go build"}},
					{Name: "build[os=darwin]", Image: "golang:1.23", Run: []string{"GOOS=darwin go build"}},
				},
			},
		},
		{
			name: "pipeline-level correlated deps",
			input: Pipeline{
				Name: "ci",
				Matrix: &Matrix{
					Dimensions: []Dimension{
						{Name: "os", Values: []string{"linux", "darwin"}},
					},
				},
				Steps: []Step{
					{Name: "build", Image: "golang:1.23", Run: []string{"GOOS=${matrix.os} go build"}},
					{Name: "test", Image: "golang:1.23", Run: []string{"GOOS=${matrix.os} go test"}, DependsOn: []string{"build"}},
				},
			},
			want: Pipeline{
				Name: "ci",
				Steps: []Step{
					{Name: "build[os=linux]", Image: "golang:1.23", Run: []string{"GOOS=linux go build"}},
					{Name: "test[os=linux]", Image: "golang:1.23", Run: []string{"GOOS=linux go test"}, DependsOn: []string{"build[os=linux]"}},
					{Name: "build[os=darwin]", Image: "golang:1.23", Run: []string{"GOOS=darwin go build"}},
					{Name: "test[os=darwin]", Image: "golang:1.23", Run: []string{"GOOS=darwin go test"}, DependsOn: []string{"build[os=darwin]"}},
				},
			},
		},
		{
			name: "step-level matrix",
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{
						Name:  "test",
						Image: "golang:${matrix.go-version}",
						Run:   []string{"go test"},
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
				Steps: []Step{
					{Name: "test[go-version=1.21]", Image: "golang:1.21", Run: []string{"go test"}},
					{Name: "test[go-version=1.22]", Image: "golang:1.22", Run: []string{"go test"}},
					{Name: "test[go-version=1.23]", Image: "golang:1.23", Run: []string{"go test"}},
				},
			},
		},
		{
			name: "step-level all-variant deps",
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{
						Name:  "test",
						Image: "golang:${matrix.go-version}",
						Run:   []string{"go test"},
						Matrix: &Matrix{
							Dimensions: []Dimension{
								{Name: "go-version", Values: []string{"1.21", "1.22"}},
							},
						},
					},
					{
						Name:      "deploy",
						Image:     "alpine:latest",
						Run:       []string{"echo deploy"},
						DependsOn: []string{"test"},
					},
				},
			},
			want: Pipeline{
				Name: "ci",
				Steps: []Step{
					{Name: "test[go-version=1.21]", Image: "golang:1.21", Run: []string{"go test"}},
					{Name: "test[go-version=1.22]", Image: "golang:1.22", Run: []string{"go test"}},
					{Name: "deploy", Image: "alpine:latest", Run: []string{"echo deploy"}, DependsOn: []string{"test[go-version=1.21]", "test[go-version=1.22]"}},
				},
			},
		},
		{
			name: "combined pipeline and step matrix",
			input: Pipeline{
				Name: "ci",
				Matrix: &Matrix{
					Dimensions: []Dimension{
						{Name: "os", Values: []string{"linux", "darwin"}},
					},
				},
				Steps: []Step{
					{Name: "build", Image: "golang:1.23", Run: []string{"GOOS=${matrix.os} go build"}},
					{
						Name:      "test",
						Image:     "golang:${matrix.go-version}",
						Run:       []string{"GOOS=${matrix.os} go test"},
						DependsOn: []string{"build"},
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
				Steps: []Step{
					{Name: "build[os=linux]", Image: "golang:1.23", Run: []string{"GOOS=linux go build"}},
					// test[os=linux] expands into two go-version variants
					{Name: "test[go-version=1.21,os=linux]", Image: "golang:1.21", Run: []string{"GOOS=linux go test"}, DependsOn: []string{"build[os=linux]"}},
					{Name: "test[go-version=1.22,os=linux]", Image: "golang:1.22", Run: []string{"GOOS=linux go test"}, DependsOn: []string{"build[os=linux]"}},
					{Name: "build[os=darwin]", Image: "golang:1.23", Run: []string{"GOOS=darwin go build"}},
					{Name: "test[go-version=1.21,os=darwin]", Image: "golang:1.21", Run: []string{"GOOS=darwin go test"}, DependsOn: []string{"build[os=darwin]"}},
					{Name: "test[go-version=1.22,os=darwin]", Image: "golang:1.22", Run: []string{"GOOS=darwin go test"}, DependsOn: []string{"build[os=darwin]"}},
				},
			},
		},
		{
			name: "variable substitution in all fields",
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{
						Name:    "build",
						Image:   "golang:${matrix.go}",
						Run:     []string{"GOARCH=${matrix.go} build"},
						Workdir: "/src/${matrix.go}",
						Mounts: []Mount{
							{Source: "./${matrix.go}", Target: "/mnt/${matrix.go}"},
						},
						Caches: []Cache{
							{ID: "cache", Target: "/cache/${matrix.go}"},
						},
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
				Steps: []Step{
					{
						Name:    "build[go=1.21]",
						Image:   "golang:1.21",
						Run:     []string{"GOARCH=1.21 build"},
						Workdir: "/src/1.21",
						Mounts: []Mount{
							{Source: "./1.21", Target: "/mnt/1.21"},
						},
						Caches: []Cache{
							{ID: "cache--build[go=1.21]", Target: "/cache/1.21"},
						},
					},
				},
			},
		},
		{
			name: "matrix variable in cache ID",
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{
						Name:  "build",
						Image: "golang:1.23",
						Run:   []string{"go build"},
						Caches: []Cache{
							{ID: "gomod-${matrix.os}", Target: "/go/pkg/mod"},
						},
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
				Steps: []Step{
					{
						Name:  "build[os=linux]",
						Image: "golang:1.23",
						Run:   []string{"go build"},
						Caches: []Cache{
							{ID: "gomod-linux--build[os=linux]", Target: "/go/pkg/mod"},
						},
					},
					{
						Name:  "build[os=darwin]",
						Image: "golang:1.23",
						Run:   []string{"go build"},
						Caches: []Cache{
							{ID: "gomod-darwin--build[os=darwin]", Target: "/go/pkg/mod"},
						},
					},
				},
			},
		},
		{
			name: "cache ID namespacing with pipeline matrix",
			input: Pipeline{
				Name: "ci",
				Matrix: &Matrix{
					Dimensions: []Dimension{
						{Name: "os", Values: []string{"linux", "darwin"}},
					},
				},
				Steps: []Step{
					{
						Name:  "build",
						Image: "golang:1.23",
						Run:   []string{"go build"},
						Caches: []Cache{
							{ID: "gomod", Target: "/go/pkg/mod"},
						},
					},
				},
			},
			want: Pipeline{
				Name: "ci",
				Steps: []Step{
					{
						Name:  "build[os=linux]",
						Image: "golang:1.23",
						Run:   []string{"go build"},
						Caches: []Cache{
							{ID: "gomod--build[os=linux]", Target: "/go/pkg/mod"},
						},
					},
					{
						Name:  "build[os=darwin]",
						Image: "golang:1.23",
						Run:   []string{"go build"},
						Caches: []Cache{
							{ID: "gomod--build[os=darwin]", Target: "/go/pkg/mod"},
						},
					},
				},
			},
		},
		{
			name: "cache ID double-namespaced across pipeline and step matrix",
			input: Pipeline{
				Name: "ci",
				Matrix: &Matrix{
					Dimensions: []Dimension{
						{Name: "os", Values: []string{"linux"}},
					},
				},
				Steps: []Step{
					{
						Name:  "test",
						Image: "golang:${matrix.go-version}",
						Run:   []string{"go test"},
						Caches: []Cache{
							{ID: "gomod", Target: "/go/pkg/mod"},
						},
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
				Steps: []Step{
					{
						Name:  "test[go-version=1.21,os=linux]",
						Image: "golang:1.21",
						Run:   []string{"go test"},
						Caches: []Cache{
							{ID: "gomod--test[os=linux]--test[go-version=1.21,os=linux]", Target: "/go/pkg/mod"},
						},
					},
				},
			},
		},
		{
			name: "platform substituted during expansion",
			input: Pipeline{
				Name: "ci",
				Matrix: &Matrix{
					Dimensions: []Dimension{
						{Name: "platform", Values: []string{"linux/amd64", "linux/arm64"}},
					},
				},
				Steps: []Step{
					{Name: "build", Image: "golang:1.23", Platform: "${matrix.platform}", Run: []string{"go version"}},
				},
			},
			want: Pipeline{
				Name: "ci",
				Steps: []Step{
					{Name: "build[platform=linux/amd64]", Image: "golang:1.23", Platform: "linux/amd64", Run: []string{"go version"}},
					{Name: "build[platform=linux/arm64]", Image: "golang:1.23", Platform: "linux/arm64", Run: []string{"go version"}},
				},
			},
		},
		{
			name:    "dimension name collision",
			wantErr: ErrDuplicateDim,
			input: Pipeline{
				Name: "ci",
				Matrix: &Matrix{
					Dimensions: []Dimension{
						{Name: "os", Values: []string{"linux"}},
					},
				},
				Steps: []Step{
					{
						Name:  "test",
						Image: "alpine",
						Run:   []string{"echo"},
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
				Steps: []Step{
					{Name: "a", Image: "alpine", Run: []string{"echo"}},
				},
			},
		},
		{
			name:    "empty step matrix",
			wantErr: ErrEmptyMatrix,
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{Name: "a", Image: "alpine", Run: []string{"echo"}, Matrix: &Matrix{}},
				},
			},
		},
		{
			name:    "empty dimension values",
			wantErr: ErrEmptyDimension,
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{
						Name: "a", Image: "alpine", Run: []string{"echo"},
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
				Steps: []Step{
					{
						Name: "a", Image: "alpine", Run: []string{"echo"},
						Matrix: &Matrix{
							Dimensions: []Dimension{{Name: "my os!", Values: []string{"linux"}}},
						},
					},
				},
			},
		},
		{
			name:    "matrix exceeds combination limit",
			wantErr: ErrMatrixTooLarge,
			input: Pipeline{
				Name: "ci",
				Steps: []Step{
					{
						Name: "a", Image: "alpine", Run: []string{"echo"},
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
				Steps: []Step{
					{
						Name: "a", Image: "alpine", Run: []string{"echo"},
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
			got, err := Expand(tt.input)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandedStepName(t *testing.T) {
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
			got := expandedStepName(tt.base, tt.combo)
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
			got := substituteVars(tt.input, tt.combo)
			assert.Equal(t, tt.want, got)
		})
	}
}
