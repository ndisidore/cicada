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
		steps  []Step
		params map[string]string
		want   []Step
	}{
		{
			name: "substitution in all fields",
			steps: []Step{{
				Name:     "test",
				Image:    "golang:${param.version}",
				Run:      []string{"go test -cover=${param.threshold} ./..."},
				Workdir:  "${param.workdir}",
				Platform: "${param.platform}",
				Mounts:   []Mount{{Source: "${param.src}", Target: "${param.dest}"}},
				Caches:   []Cache{{ID: "mod", Target: "${param.cache-dir}"}},
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
			want: []Step{{
				Name:     "test",
				Image:    "golang:1.23",
				Run:      []string{"go test -cover=80 ./..."},
				Workdir:  "/src",
				Platform: "linux/amd64",
				Mounts:   []Mount{{Source: ".", Target: "/app"}},
				Caches:   []Cache{{ID: "mod", Target: "/go/pkg/mod"}},
			}},
		},
		{
			name: "matrix vars pass through",
			steps: []Step{{
				Name:  "test",
				Image: "golang:${param.version}",
				Run:   []string{"echo ${matrix.os}"},
			}},
			params: map[string]string{"version": "1.23"},
			want: []Step{{
				Name:  "test",
				Image: "golang:1.23",
				Run:   []string{"echo ${matrix.os}"},
			}},
		},
		{
			name:   "empty params is noop",
			steps:  []Step{{Name: "a", Image: "${param.x}"}},
			params: map[string]string{},
			want:   []Step{{Name: "a", Image: "${param.x}"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := SubstituteParams(tt.steps, tt.params)
			assert.Equal(t, tt.want, got)
		})
	}
}
