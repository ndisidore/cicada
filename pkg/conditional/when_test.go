package conditional

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWhenEvaluate(t *testing.T) {
	t.Parallel()

	ctx := Context{
		Getenv: func(key string) string {
			m := map[string]string{"CI": "true", "SKIP": "no"}
			return m[key]
		},
		Branch:      "main",
		Tag:         "v1.0.0",
		PipelineEnv: map[string]string{"DEPLOY": "prod", "REGION": "us-east-1"},
		Matrix:      map[string]string{"platform": "linux/amd64", "go-version": "1.25"},
	}

	tests := []struct {
		name string
		expr string
		want bool
	}{
		{name: "branch match", expr: `branch == "main"`, want: true},
		{name: "branch mismatch", expr: `branch == "develop"`, want: false},
		{name: "tag match", expr: `tag == "v1.0.0"`, want: true},
		{name: "tag regex", expr: `tag.matches("^v\\d+\\.\\d+\\.\\d+$")`, want: true},
		{name: "env match", expr: `env("DEPLOY") == "prod"`, want: true},
		{name: "env mismatch", expr: `env("DEPLOY") == "staging"`, want: false},
		{name: "env unset returns empty", expr: `env("NONEXISTENT") == ""`, want: true},
		{name: "hostEnv match", expr: `hostEnv("CI") == "true"`, want: true},
		{name: "hostEnv mismatch", expr: `hostEnv("CI") == "false"`, want: false},
		{name: "hostEnv unset returns empty", expr: `hostEnv("NONEXISTENT") == ""`, want: true},
		{name: "AND true", expr: `branch == "main" && hostEnv("CI") == "true"`, want: true},
		{name: "AND false", expr: `branch == "main" && hostEnv("CI") == "false"`, want: false},
		{name: "OR true left", expr: `branch == "main" || tag == ""`, want: true},
		{name: "OR true right", expr: `branch == "dev" || tag == "v1.0.0"`, want: true},
		{name: "OR false", expr: `branch == "dev" || tag == "v2.0.0"`, want: false},
		{name: "NOT", expr: `hostEnv("SKIP") != "yes"`, want: true},
		{name: "complex", expr: `branch.matches("^(main|release/.*)$") && hostEnv("SKIP") != "yes"`, want: true},
		{name: "env and hostEnv combined", expr: `env("DEPLOY") == "prod" && hostEnv("CI") == "true"`, want: true},
		{name: "matrix match", expr: `matrix("platform") == "linux/amd64"`, want: true},
		{name: "matrix mismatch", expr: `matrix("platform") == "linux/arm64"`, want: false},
		{name: "matrix unset returns empty", expr: `matrix("nonexistent") == ""`, want: true},
		{name: "matrix with branch", expr: `matrix("go-version") == "1.25" && branch == "main"`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := &When{Expression: tt.expr}
			got, err := w.Evaluate(ctx)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("invalid expression returns error", func(t *testing.T) {
		t.Parallel()
		w := &When{Expression: `branch ==`}
		_, err := w.Evaluate(ctx)
		require.ErrorIs(t, err, ErrInvalidWhenExpr)
	})

	t.Run("non-bool expression returns error", func(t *testing.T) {
		t.Parallel()
		w := &When{Expression: `env("DEPLOY")`}
		_, err := w.Evaluate(ctx)
		require.ErrorIs(t, err, ErrWhenNotBool)
	})
}

func TestEvaluateDeferred(t *testing.T) {
	t.Parallel()

	ctx := Context{
		Getenv:      func(string) string { return "" },
		Branch:      "main",
		Tag:         "",
		PipelineEnv: map[string]string{"STAGE": "prod"},
		Matrix:      map[string]string{"os": "linux"},
	}

	tests := []struct {
		name       string
		expr       string
		depOutputs map[string]map[string]string
		want       bool
	}{
		{
			name: "output match",
			expr: `output("build", "status") == "ok"`,
			depOutputs: map[string]map[string]string{
				"build": {"status": "ok"},
			},
			want: true,
		},
		{
			name: "output mismatch",
			expr: `output("build", "status") == "ok"`,
			depOutputs: map[string]map[string]string{
				"build": {"status": "fail"},
			},
			want: false,
		},
		{
			name:       "missing output key returns empty",
			expr:       `output("build", "missing") == ""`,
			depOutputs: map[string]map[string]string{"build": {"status": "ok"}},
			want:       true,
		},
		{
			name:       "skipped dep returns empty",
			expr:       `output("skipped", "key") == ""`,
			depOutputs: map[string]map[string]string{},
			want:       true,
		},
		{
			name: "combined static and output",
			expr: `branch == "main" && output("check", "ready") == "yes"`,
			depOutputs: map[string]map[string]string{
				"check": {"ready": "yes"},
			},
			want: true,
		},
		{
			name: "env with output",
			expr: `env("STAGE") == "prod" && output("build", "status") == "ok"`,
			depOutputs: map[string]map[string]string{
				"build": {"status": "ok"},
			},
			want: true,
		},
		{
			name: "matrix with output",
			expr: `matrix("os") == "linux" && output("build", "status") == "ok"`,
			depOutputs: map[string]map[string]string{
				"build": {"status": "ok"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := &When{Expression: tt.expr, Deferred: true}
			got, err := w.EvaluateDeferred(ctx, tt.depOutputs)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("invalid expression returns error", func(t *testing.T) {
		t.Parallel()
		w := &When{Expression: `branch ==`, Deferred: true}
		_, err := w.EvaluateDeferred(ctx, nil)
		require.ErrorIs(t, err, ErrInvalidWhenExpr)
	})

	t.Run("non-bool expression returns error", func(t *testing.T) {
		t.Parallel()
		w := &When{Expression: `output("build", "status")`, Deferred: true}
		_, err := w.EvaluateDeferred(ctx, map[string]map[string]string{"build": {"status": "ok"}})
		require.ErrorIs(t, err, ErrWhenNotBool)
	})

	t.Run("non-deferred expression returns error", func(t *testing.T) {
		t.Parallel()
		w := &When{Expression: `branch == "main"`, Deferred: false}
		_, err := w.EvaluateDeferred(ctx, nil)
		require.ErrorIs(t, err, ErrNotDeferredEvaluate)
	})
}
