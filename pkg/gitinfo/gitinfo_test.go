package gitinfo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubEnv builds a getenv function backed by a static map.
func stubEnv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func TestDetectBranch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "GITHUB_HEAD_REF takes priority",
			env: map[string]string{
				"GITHUB_HEAD_REF":  "feat/pr-branch",
				"GITHUB_REF_NAME":  "main",
				"CI_COMMIT_BRANCH": "gitlab-branch",
				"CIRCLE_BRANCH":    "circle-branch",
			},
			want: "feat/pr-branch",
		},
		{
			name: "GITHUB_REF_NAME non-tag",
			env: map[string]string{
				"GITHUB_REF_NAME": "main",
				"GITHUB_REF_TYPE": "branch",
			},
			want: "main",
		},
		{
			name: "GITHUB_REF_NAME skipped when type is tag",
			env: map[string]string{
				"GITHUB_REF_NAME":  "v1.0.0",
				"GITHUB_REF_TYPE":  "tag",
				"CI_COMMIT_BRANCH": "fallback-branch",
			},
			want: "fallback-branch",
		},
		{
			name: "CI_COMMIT_BRANCH (GitLab)",
			env:  map[string]string{"CI_COMMIT_BRANCH": "gitlab-main"},
			want: "gitlab-main",
		},
		{
			name: "CIRCLE_BRANCH (CircleCI)",
			env:  map[string]string{"CIRCLE_BRANCH": "circle-main"},
			want: "circle-main",
		},
		{
			name: "priority order: GITHUB_HEAD_REF > GITHUB_REF_NAME",
			env: map[string]string{
				"GITHUB_HEAD_REF": "pr-branch",
				"GITHUB_REF_NAME": "main",
				"GITHUB_REF_TYPE": "branch",
			},
			want: "pr-branch",
		},
		{
			name: "priority order: GITHUB_REF_NAME > CI_COMMIT_BRANCH",
			env: map[string]string{
				"GITHUB_REF_NAME":  "gh-branch",
				"GITHUB_REF_TYPE":  "branch",
				"CI_COMMIT_BRANCH": "gl-branch",
			},
			want: "gh-branch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DetectBranch(context.Background(), stubEnv(tt.env))
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("cancelled context returns empty on git fallback", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := DetectBranch(ctx, stubEnv(nil))
		assert.Empty(t, got)
	})
}

func TestDetectTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "GITHUB_REF_NAME with tag type",
			env: map[string]string{
				"GITHUB_REF_NAME": "v1.0.0",
				"GITHUB_REF_TYPE": "tag",
			},
			want: "v1.0.0",
		},
		{
			name: "GITHUB_REF_NAME skipped when type is branch",
			env: map[string]string{
				"GITHUB_REF_NAME": "main",
				"GITHUB_REF_TYPE": "branch",
				"CI_COMMIT_TAG":   "v-fallback",
			},
			want: "v-fallback",
		},
		{
			name: "CI_COMMIT_TAG (GitLab)",
			env:  map[string]string{"CI_COMMIT_TAG": "v2.0.0"},
			want: "v2.0.0",
		},
		{
			name: "CIRCLE_TAG (CircleCI)",
			env:  map[string]string{"CIRCLE_TAG": "v3.0.0"},
			want: "v3.0.0",
		},
		{
			name: "priority order: GITHUB_REF_NAME tag > CI_COMMIT_TAG",
			env: map[string]string{
				"GITHUB_REF_NAME": "v1.0.0",
				"GITHUB_REF_TYPE": "tag",
				"CI_COMMIT_TAG":   "v2.0.0",
			},
			want: "v1.0.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DetectTag(context.Background(), stubEnv(tt.env))
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("cancelled context returns empty on git fallback", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := DetectTag(ctx, stubEnv(nil))
		assert.Empty(t, got)
	})
}
