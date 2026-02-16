package conditional

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateWhenExpr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		expr     string
		deferred bool
		wantErr  error // nil or sentinel
	}{
		{
			name: "simple branch comparison",
			expr: `branch == "main"`,
		},
		{
			name: "tag matches regex",
			expr: `tag.matches("^v\\d+\\.\\d+\\.\\d+$")`,
		},
		{
			name: "env function",
			expr: `env("CI") == "true"`,
		},
		{
			name:     "output function marks deferred",
			expr:     `output("build", "status") == "ok"`,
			deferred: true,
		},
		{
			name:     "mixed static and output is deferred",
			expr:     `branch == "main" && output("check", "ready") == "true"`,
			deferred: true,
		},
		{
			name: "complex OR expression",
			expr: `branch == "main" || tag.matches("^v\\d+")`,
		},
		{
			name: "negation",
			expr: `env("SKIP_DEPLOY") != "true"`,
		},
		{
			name: "AND expression",
			expr: `branch == "main" && env("CI") == "true"`,
		},
		{
			name: "ternary expression",
			expr: `branch == "main" ? true : false`,
		},
		{
			name: "string contains",
			expr: `branch.contains("release")`,
		},
		{
			name: "string startsWith",
			expr: `branch.startsWith("feat/")`,
		},
		{
			name:    "invalid syntax",
			expr:    `branch ==`,
			wantErr: ErrInvalidWhenExpr,
		},
		{
			name:    "unknown variable",
			expr:    `unknown_var == "foo"`,
			wantErr: ErrInvalidWhenExpr,
		},
		{
			name:    "non-bool return type",
			expr:    `branch`,
			wantErr: ErrWhenNotBool,
		},
		{
			name:    "non-bool return from env",
			expr:    `env("CI")`,
			wantErr: ErrWhenNotBool,
		},
		{
			name: "hostEnv function",
			expr: `hostEnv("CI") == "true"`,
		},
		{
			name: "hostEnv negation",
			expr: `hostEnv("SKIP_DEPLOY") != "true"`,
		},
		{
			name: "matrix function",
			expr: `matrix("platform") == "linux/amd64"`,
		},
		{
			name: "matrix in compound expression",
			expr: `matrix("os") == "linux" && branch == "main"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deferred, err := ValidateWhenExpr(tt.expr)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.deferred, deferred)
		})
	}
}

func TestEvaluateDeferredGuard(t *testing.T) {
	t.Parallel()

	w := &When{
		Expression: `output("build", "status") == "ok"`,
		Deferred:   true,
	}
	ctx := Context{
		Getenv:      func(string) string { return "" },
		PipelineEnv: map[string]string{},
	}

	_, err := w.Evaluate(ctx)
	require.ErrorIs(t, err, ErrDeferredEvaluate)
}
