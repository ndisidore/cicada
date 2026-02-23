package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

func TestValidateParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		defs     []pm.ParamDef
		provided map[string]string
		wantErr  error
	}{
		{
			name: "all required provided",
			defs: []pm.ParamDef{
				{Name: "version", Required: true},
			},
			provided: map[string]string{"version": "1.23"},
		},
		{
			name: "optional not provided uses default",
			defs: []pm.ParamDef{
				{Name: "version", Default: "1.23"},
			},
			provided: map[string]string{},
		},
		{
			name: "required missing",
			defs: []pm.ParamDef{
				{Name: "threshold", Required: true},
			},
			provided: map[string]string{},
			wantErr:  pm.ErrMissingParam,
		},
		{
			name: "unknown param",
			defs: []pm.ParamDef{
				{Name: "version", Required: true},
			},
			provided: map[string]string{"version": "1.23", "extra": "bad"},
			wantErr:  pm.ErrUnknownParam,
		},
		{
			name: "duplicate param definition",
			defs: []pm.ParamDef{
				{Name: "version", Required: true},
				{Name: "version", Default: "1.23"},
			},
			provided: map[string]string{"version": "1.23"},
			wantErr:  pm.ErrDuplicateParam,
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
		defs     []pm.ParamDef
		provided map[string]string
		want     map[string]string
		wantErr  error
	}{
		{
			name: "all provided",
			defs: []pm.ParamDef{
				{Name: "version", Required: true},
				{Name: "threshold", Required: true},
			},
			provided: map[string]string{"version": "1.23", "threshold": "80"},
			want:     map[string]string{"version": "1.23", "threshold": "80"},
		},
		{
			name: "defaults used",
			defs: []pm.ParamDef{
				{Name: "version", Default: "1.23"},
				{Name: "threshold", Default: "70"},
			},
			provided: map[string]string{},
			want:     map[string]string{"version": "1.23", "threshold": "70"},
		},
		{
			name: "provided overrides default",
			defs: []pm.ParamDef{
				{Name: "version", Default: "1.22"},
			},
			provided: map[string]string{"version": "1.23"},
			want:     map[string]string{"version": "1.23"},
		},
		{
			name: "required missing propagates error",
			defs: []pm.ParamDef{
				{Name: "version", Required: true},
			},
			provided: map[string]string{},
			wantErr:  pm.ErrMissingParam,
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
		jobs   []pm.Job
		params map[string]string
		want   []pm.Job
	}{
		{
			name: "substitution in all fields",
			jobs: []pm.Job{{
				Name:     "test",
				Image:    "golang:${param.version}",
				Workdir:  "${param.workdir}",
				Platform: "${param.platform}",
				Mounts:   []pm.Mount{{Source: "${param.src}", Target: "${param.dest}"}},
				Caches:   []pm.Cache{{ID: "mod", Target: "${param.cache-dir}"}},
				Steps: []pm.Step{{
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
			want: []pm.Job{{
				Name:     "test",
				Image:    "golang:1.23",
				Workdir:  "/src",
				Platform: "linux/amd64",
				Mounts:   []pm.Mount{{Source: ".", Target: "/app"}},
				Caches:   []pm.Cache{{ID: "mod", Target: "/go/pkg/mod"}},
				Steps: []pm.Step{{
					Name: "test",
					Run:  []string{"go test -cover=80 ./..."},
				}},
			}},
		},
		{
			name: "matrix vars pass through",
			jobs: []pm.Job{{
				Name:  "test",
				Image: "golang:${param.version}",
				Steps: []pm.Step{{
					Name: "test",
					Run:  []string{"echo ${matrix.os}"},
				}},
			}},
			params: map[string]string{"version": "1.23"},
			want: []pm.Job{{
				Name:  "test",
				Image: "golang:1.23",
				Steps: []pm.Step{{
					Name: "test",
					Run:  []string{"echo ${matrix.os}"},
				}},
			}},
		},
		{
			name: "substitution in job matrix dimensions",
			jobs: []pm.Job{{
				Name:  "test",
				Image: "alpine",
				Matrix: &pm.Matrix{Dimensions: []pm.Dimension{
					{Name: "go-version", Values: []string{"${param.min-go}", "${param.max-go}"}},
				}},
				Steps: []pm.Step{{Name: "run", Run: []string{"go test"}}},
			}},
			params: map[string]string{"min-go": "1.22", "max-go": "1.23"},
			want: []pm.Job{{
				Name:  "test",
				Image: "alpine",
				Matrix: &pm.Matrix{Dimensions: []pm.Dimension{
					{Name: "go-version", Values: []string{"1.22", "1.23"}},
				}},
				Steps: []pm.Step{{Name: "run", Run: []string{"go test"}}},
			}},
		},
		{
			name: "nil matrix preserved",
			jobs: []pm.Job{{
				Name:  "test",
				Image: "alpine",
				Steps: []pm.Step{{Name: "run", Run: []string{"echo hi"}}},
			}},
			params: map[string]string{"x": "y"},
			want: []pm.Job{{
				Name:  "test",
				Image: "alpine",
				Steps: []pm.Step{{Name: "run", Run: []string{"echo hi"}}},
			}},
		},
		{
			name: "substitution in publish image",
			jobs: []pm.Job{{
				Name:    "build",
				Image:   "alpine",
				Publish: &pm.Publish{Image: "ghcr.io/${param.org}/app:${param.tag}", Push: true, Insecure: true},
				Steps:   []pm.Step{{Name: "build", Run: []string{"echo build"}}},
			}},
			params: map[string]string{"org": "myuser", "tag": "v1.0"},
			want: []pm.Job{{
				Name:    "build",
				Image:   "alpine",
				Publish: &pm.Publish{Image: "ghcr.io/myuser/app:v1.0", Push: true, Insecure: true},
				Steps:   []pm.Step{{Name: "build", Run: []string{"echo build"}}},
			}},
		},
		{
			name: "nil publish preserved",
			jobs: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{{Name: "build", Run: []string{"echo build"}}},
			}},
			params: map[string]string{"x": "y"},
			want: []pm.Job{{
				Name:  "build",
				Image: "alpine",
				Steps: []pm.Step{{Name: "build", Run: []string{"echo build"}}},
			}},
		},
		{
			name: "empty params is noop",
			jobs: []pm.Job{{
				Name:  "a",
				Image: "${param.x}",
				Steps: []pm.Step{{Name: "a"}},
			}},
			params: map[string]string{},
			want: []pm.Job{{
				Name:  "a",
				Image: "${param.x}",
				Steps: []pm.Step{{Name: "a"}},
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
