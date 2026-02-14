package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		defs     []ParamDef
		provided map[string]string
		wantErr  error
	}{
		{
			name: "all required provided",
			defs: []ParamDef{
				{Name: "version", Required: true},
			},
			provided: map[string]string{"version": "1.23"},
		},
		{
			name: "optional not provided uses default",
			defs: []ParamDef{
				{Name: "version", Default: "1.23"},
			},
			provided: map[string]string{},
		},
		{
			name: "required missing",
			defs: []ParamDef{
				{Name: "threshold", Required: true},
			},
			provided: map[string]string{},
			wantErr:  ErrMissingParam,
		},
		{
			name: "unknown param",
			defs: []ParamDef{
				{Name: "version", Required: true},
			},
			provided: map[string]string{"version": "1.23", "extra": "bad"},
			wantErr:  ErrUnknownParam,
		},
		{
			name: "duplicate param definition",
			defs: []ParamDef{
				{Name: "version", Required: true},
				{Name: "version", Default: "1.23"},
			},
			provided: map[string]string{"version": "1.23"},
			wantErr:  ErrDuplicateParam,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateParams(tt.defs, tt.provided)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestResolveParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		defs     []ParamDef
		provided map[string]string
		want     map[string]string
		wantErr  error
	}{
		{
			name: "all provided",
			defs: []ParamDef{
				{Name: "version", Required: true},
				{Name: "threshold", Required: true},
			},
			provided: map[string]string{"version": "1.23", "threshold": "80"},
			want:     map[string]string{"version": "1.23", "threshold": "80"},
		},
		{
			name: "defaults used",
			defs: []ParamDef{
				{Name: "version", Default: "1.23"},
				{Name: "threshold", Default: "70"},
			},
			provided: map[string]string{},
			want:     map[string]string{"version": "1.23", "threshold": "70"},
		},
		{
			name: "provided overrides default",
			defs: []ParamDef{
				{Name: "version", Default: "1.22"},
			},
			provided: map[string]string{"version": "1.23"},
			want:     map[string]string{"version": "1.23"},
		},
		{
			name: "required missing propagates error",
			defs: []ParamDef{
				{Name: "version", Required: true},
			},
			provided: map[string]string{},
			wantErr:  ErrMissingParam,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveParams(tt.defs, tt.provided)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSubstituteParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		jobs   []Job
		params map[string]string
		want   []Job
	}{
		{
			name: "substitution in all fields",
			jobs: []Job{{
				Name:     "test",
				Image:    "golang:${param.version}",
				Workdir:  "${param.workdir}",
				Platform: "${param.platform}",
				Mounts:   []Mount{{Source: "${param.src}", Target: "${param.dest}"}},
				Caches:   []Cache{{ID: "mod", Target: "${param.cache-dir}"}},
				Steps: []Step{{
					Name: "test",
					Run:  []string{"go test -cover=${param.threshold} ./..."},
				}},
			}},
			params: map[string]string{
				"version":   "1.23",
				"threshold": "80",
				"workdir":   "/src",
				"platform":  "linux/amd64",
				"src":       ".",
				"dest":      "/app",
				"cache-dir": "/go/pkg/mod",
			},
			want: []Job{{
				Name:     "test",
				Image:    "golang:1.23",
				Workdir:  "/src",
				Platform: "linux/amd64",
				Mounts:   []Mount{{Source: ".", Target: "/app"}},
				Caches:   []Cache{{ID: "mod", Target: "/go/pkg/mod"}},
				Steps: []Step{{
					Name: "test",
					Run:  []string{"go test -cover=80 ./..."},
				}},
			}},
		},
		{
			name: "matrix vars pass through",
			jobs: []Job{{
				Name:  "test",
				Image: "golang:${param.version}",
				Steps: []Step{{
					Name: "test",
					Run:  []string{"echo ${matrix.os}"},
				}},
			}},
			params: map[string]string{"version": "1.23"},
			want: []Job{{
				Name:  "test",
				Image: "golang:1.23",
				Steps: []Step{{
					Name: "test",
					Run:  []string{"echo ${matrix.os}"},
				}},
			}},
		},
		{
			name: "substitution in job matrix dimensions",
			jobs: []Job{{
				Name:  "test",
				Image: "alpine",
				Matrix: &Matrix{Dimensions: []Dimension{
					{Name: "go-version", Values: []string{"${param.min-go}", "${param.max-go}"}},
				}},
				Steps: []Step{{Name: "run", Run: []string{"go test"}}},
			}},
			params: map[string]string{"min-go": "1.22", "max-go": "1.23"},
			want: []Job{{
				Name:  "test",
				Image: "alpine",
				Matrix: &Matrix{Dimensions: []Dimension{
					{Name: "go-version", Values: []string{"1.22", "1.23"}},
				}},
				Steps: []Step{{Name: "run", Run: []string{"go test"}}},
			}},
		},
		{
			name: "nil matrix preserved",
			jobs: []Job{{
				Name:  "test",
				Image: "alpine",
				Steps: []Step{{Name: "run", Run: []string{"echo hi"}}},
			}},
			params: map[string]string{"x": "y"},
			want: []Job{{
				Name:  "test",
				Image: "alpine",
				Steps: []Step{{Name: "run", Run: []string{"echo hi"}}},
			}},
		},
		{
			name: "substitution in publish image",
			jobs: []Job{{
				Name:    "build",
				Image:   "alpine",
				Publish: &Publish{Image: "ghcr.io/${param.org}/app:${param.tag}", Push: true, Insecure: true},
				Steps:   []Step{{Name: "build", Run: []string{"echo build"}}},
			}},
			params: map[string]string{"org": "myuser", "tag": "v1.0"},
			want: []Job{{
				Name:    "build",
				Image:   "alpine",
				Publish: &Publish{Image: "ghcr.io/myuser/app:v1.0", Push: true, Insecure: true},
				Steps:   []Step{{Name: "build", Run: []string{"echo build"}}},
			}},
		},
		{
			name: "nil publish preserved",
			jobs: []Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []Step{{Name: "build", Run: []string{"echo build"}}},
			}},
			params: map[string]string{"x": "y"},
			want: []Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []Step{{Name: "build", Run: []string{"echo build"}}},
			}},
		},
		{
			name: "empty params is noop",
			jobs: []Job{{
				Name:  "a",
				Image: "${param.x}",
				Steps: []Step{{Name: "a"}},
			}},
			params: map[string]string{},
			want: []Job{{
				Name:  "a",
				Image: "${param.x}",
				Steps: []Step{{Name: "a"}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SubstituteParams(tt.jobs, tt.params)
			assert.Equal(t, tt.want, got)
		})
	}
}
