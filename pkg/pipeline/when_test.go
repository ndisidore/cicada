package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/pkg/conditional"
	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

func TestEvaluateConditions(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	wctx := conditional.Context{
		Getenv: func(key string) string {
			if key == "DEPLOY" {
				return "false"
			}
			return ""
		},
		Branch: "main",
		Tag:    "",
	}

	t.Run("job skip removes job and cleans refs", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{Name: "build", Image: "alpine", Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}}},
				{
					Name: "deploy", Image: "alpine",
					When:      &conditional.When{Expression: `hostEnv("DEPLOY") == "true"`},
					DependsOn: []string{"build"},
					Steps:     []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
				{
					Name: "notify", Image: "alpine",
					DependsOn: []string{"deploy"},
					Artifacts: []pm.Artifact{{From: "deploy", Source: "/out", Target: "/in"}},
					Steps:     []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 2)
		assert.Equal(t, "build", result.Pipeline.Jobs[0].Name)
		assert.Equal(t, "notify", result.Pipeline.Jobs[1].Name)
		assert.Empty(t, result.Pipeline.Jobs[1].DependsOn)
		assert.Empty(t, result.Pipeline.Jobs[1].Artifacts)
		assert.Equal(t, []string{"deploy"}, result.Skipped)
	})

	t.Run("step skip removes step", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "test", Image: "alpine",
					Steps: []pm.Step{
						{Name: "fast", Run: []string{"echo fast"}},
						{
							Name: "slow", Run: []string{"echo slow"},
							When: &conditional.When{Expression: `hostEnv("DEPLOY") == "true"`},
						},
					},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 1)
		require.Len(t, result.Pipeline.Jobs[0].Steps, 1)
		assert.Equal(t, "fast", result.Pipeline.Jobs[0].Steps[0].Name)
		assert.Empty(t, result.Skipped)
		assert.Equal(t, map[string][]string{"test": {"slow"}}, result.SkippedSteps)
	})

	t.Run("all steps skipped removes job", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "conditional", Image: "alpine",
					Steps: []pm.Step{
						{
							Name: "only-deploy", Run: []string{"echo"},
							When: &conditional.When{Expression: `hostEnv("DEPLOY") == "true"`},
						},
					},
				},
				{
					Name: "downstream", Image: "alpine",
					DependsOn: []string{"conditional"},
					Steps:     []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 1)
		assert.Equal(t, "downstream", result.Pipeline.Jobs[0].Name)
		assert.Empty(t, result.Pipeline.Jobs[0].DependsOn)
		assert.Equal(t, []string{"conditional"}, result.Skipped)
		assert.Equal(t, map[string][]string{"conditional": {"only-deploy"}}, result.SkippedSteps)
	})

	t.Run("deferred conditions are skipped", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{Name: "check", Image: "alpine", Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}}},
				{
					Name: "deploy", Image: "alpine",
					When:      &conditional.When{Expression: `output("check", "ready") == "yes"`, Deferred: true},
					DependsOn: []string{"check"},
					Steps:     []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 2)
		assert.NotNil(t, result.Pipeline.Jobs[1].When)
		assert.Empty(t, result.Skipped)
	})

	t.Run("no when always runs", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{Name: "always", Image: "alpine", Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}}},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		assert.Len(t, result.Pipeline.Jobs, 1)
		assert.Empty(t, result.Skipped)
		assert.Empty(t, result.SkippedSteps)
	})

	t.Run("when evaluates to true keeps job", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "build", Image: "alpine",
					When:  &conditional.When{Expression: `branch == "main"`},
					Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 1)
		assert.Equal(t, "build", result.Pipeline.Jobs[0].Name)
		assert.Empty(t, result.Skipped)
	})

	t.Run("clears TopoOrder", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs:      []pm.Job{{Name: "a", Image: "alpine", Steps: []pm.Step{{Name: "s", Run: []string{"echo"}}}}},
			TopoOrder: []int{0},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		assert.Nil(t, result.Pipeline.TopoOrder)
	})

	t.Run("step artifact from skipped job cleaned", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "provider", Image: "alpine",
					When:  &conditional.When{Expression: `hostEnv("DEPLOY") == "true"`},
					Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
				{
					Name: "consumer", Image: "alpine",
					DependsOn: []string{"provider"},
					Steps: []pm.Step{
						{
							Name: "s1", Run: []string{"echo"},
							Artifacts: []pm.Artifact{{From: "provider", Source: "/out", Target: "/in"}},
						},
					},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 1)
		assert.Empty(t, result.Pipeline.Jobs[0].Steps[0].Artifacts)
		assert.Equal(t, []string{"provider"}, result.Skipped)
	})

	t.Run("invalid job condition returns ErrJobCondition", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "bad", Image: "alpine",
					When:  &conditional.When{Expression: `invalid!!!`},
					Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		_, err := EvaluateConditions(ctx, p, wctx)
		require.ErrorIs(t, err, pm.ErrJobCondition)
	})

	t.Run("matrix condition skips non-matching variant", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "deploy[os=linux]", Image: "alpine",
					MatrixValues: map[string]string{"os": "linux"},
					When:         &conditional.When{Expression: `matrix("os") == "darwin"`},
					Steps:        []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
				{
					Name: "deploy[os=darwin]", Image: "alpine",
					MatrixValues: map[string]string{"os": "darwin"},
					When:         &conditional.When{Expression: `matrix("os") == "darwin"`},
					Steps:        []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 1)
		assert.Equal(t, "deploy[os=darwin]", result.Pipeline.Jobs[0].Name)
		assert.Equal(t, []string{"deploy[os=linux]"}, result.Skipped)
	})

	t.Run("matrix step condition uses job MatrixValues", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "build[os=linux]", Image: "alpine",
					MatrixValues: map[string]string{"os": "linux"},
					Steps: []pm.Step{
						{Name: "compile", Run: []string{"echo compile"}},
						{
							Name: "sign", Run: []string{"echo sign"},
							When: &conditional.When{Expression: `matrix("os") == "darwin"`},
						},
					},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 1)
		require.Len(t, result.Pipeline.Jobs[0].Steps, 1)
		assert.Equal(t, "compile", result.Pipeline.Jobs[0].Steps[0].Name)
		assert.Equal(t, map[string][]string{"build[os=linux]": {"sign"}}, result.SkippedSteps)
	})

	t.Run("invalid step condition returns ErrStepCondition", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Jobs: []pm.Job{
				{
					Name: "build", Image: "alpine",
					Steps: []pm.Step{{
						Name: "bad", Run: []string{"echo"},
						When: &conditional.When{Expression: `invalid!!!`},
					}},
				},
			},
		}

		_, err := EvaluateConditions(ctx, p, wctx)
		require.ErrorIs(t, err, pm.ErrStepCondition)
	})

	t.Run("env reads pipeline-declared env at job level", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Env: []pm.EnvVar{{Key: "STAGE", Value: "prod"}},
			Jobs: []pm.Job{
				{
					Name: "deploy", Image: "alpine",
					When:  &conditional.When{Expression: `env("STAGE") == "prod"`},
					Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		assert.Len(t, result.Pipeline.Jobs, 1)
		assert.Empty(t, result.Skipped)
	})

	t.Run("env reads job-level env overriding pipeline env", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Env: []pm.EnvVar{{Key: "STAGE", Value: "dev"}},
			Jobs: []pm.Job{
				{
					Name: "deploy", Image: "alpine",
					Env:   []pm.EnvVar{{Key: "STAGE", Value: "prod"}},
					When:  &conditional.When{Expression: `env("STAGE") == "prod"`},
					Steps: []pm.Step{{Name: "s1", Run: []string{"echo"}}},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		assert.Len(t, result.Pipeline.Jobs, 1)
		assert.Empty(t, result.Skipped)
	})

	t.Run("env reads step-level env overriding job env", func(t *testing.T) {
		t.Parallel()
		p := pm.Pipeline{
			Env: []pm.EnvVar{{Key: "STAGE", Value: "dev"}},
			Jobs: []pm.Job{
				{
					Name: "test", Image: "alpine",
					Env: []pm.EnvVar{{Key: "STAGE", Value: "staging"}},
					Steps: []pm.Step{
						{Name: "keep", Run: []string{"echo"}},
						{
							Name: "skip", Run: []string{"echo"},
							Env:  []pm.EnvVar{{Key: "STAGE", Value: "prod"}},
							When: &conditional.When{Expression: `env("STAGE") == "dev"`},
						},
					},
				},
			},
		}

		result, err := EvaluateConditions(ctx, p, wctx)
		require.NoError(t, err)
		require.Len(t, result.Pipeline.Jobs, 1)
		require.Len(t, result.Pipeline.Jobs[0].Steps, 1)
		assert.Equal(t, "keep", result.Pipeline.Jobs[0].Steps[0].Name)
		assert.Equal(t, map[string][]string{"test": {"skip"}}, result.SkippedSteps)
	})
}
