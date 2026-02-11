package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		groups  []StepGroup
		want    []Step
		wantErr error
	}{
		{
			name: "no conflicts",
			groups: []StepGroup{
				{Steps: []Step{{Name: "a"}}, Origin: "a.kdl"},
				{Steps: []Step{{Name: "b"}}, Origin: "b.kdl"},
			},
			want: []Step{{Name: "a"}, {Name: "b"}},
		},
		{
			name: "error on duplicate",
			groups: []StepGroup{
				{Steps: []Step{{Name: "a"}}, Origin: "a.kdl"},
				{Steps: []Step{{Name: "a"}}, Origin: "b.kdl", OnConflict: ConflictError},
			},
			wantErr: ErrDuplicateStep,
		},
		{
			name: "skip on duplicate",
			groups: []StepGroup{
				{Steps: []Step{{Name: "a", Image: "first"}}, Origin: "a.kdl"},
				{Steps: []Step{{Name: "a", Image: "second"}}, Origin: "b.kdl", OnConflict: ConflictSkip},
			},
			want: []Step{{Name: "a", Image: "first"}},
		},
		{
			name: "multiple groups with mixed strategies",
			groups: []StepGroup{
				{Steps: []Step{{Name: "a"}, {Name: "b"}}, Origin: "inline"},
				{Steps: []Step{{Name: "c"}, {Name: "a"}}, Origin: "inc.kdl", OnConflict: ConflictSkip},
			},
			want: []Step{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		},
		{
			name:   "empty groups",
			groups: []StepGroup{},
			want:   []Step{},
		},
		{
			name: "ordering preserved across groups",
			groups: []StepGroup{
				{Steps: []Step{{Name: "c"}, {Name: "a"}}, Origin: "first"},
				{Steps: []Step{{Name: "b"}, {Name: "d"}}, Origin: "second"},
			},
			want: []Step{{Name: "c"}, {Name: "a"}, {Name: "b"}, {Name: "d"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MergeSteps(tt.groups)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTerminalSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		steps []Step
		want  []string
	}{
		{
			name:  "single step",
			steps: []Step{{Name: "a"}},
			want:  []string{"a"},
		},
		{
			name: "chain returns last",
			steps: []Step{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"b"}},
			},
			want: []string{"c"},
		},
		{
			name: "diamond DAG returns single terminal",
			steps: []Step{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"a"}},
				{Name: "d", DependsOn: []string{"b", "c"}},
			},
			want: []string{"d"},
		},
		{
			name: "independent steps are all terminal",
			steps: []Step{
				{Name: "a"},
				{Name: "b"},
				{Name: "c"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "multiple terminals in partial DAG",
			steps: []Step{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"a"}},
			},
			want: []string{"b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := TerminalSteps(tt.steps)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		steps   []Step
		aliases map[string][]string
		want    []Step
		wantErr error
	}{
		{
			name: "single alias to single step",
			steps: []Step{
				{Name: "lint-step"},
				{Name: "build", DependsOn: []string{"lint"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			want: []Step{
				{Name: "lint-step"},
				{Name: "build", DependsOn: []string{"lint-step"}},
			},
		},
		{
			name: "alias to multiple terminal steps",
			steps: []Step{
				{Name: "unit-test"},
				{Name: "integration-test"},
				{Name: "build", DependsOn: []string{"tests"}},
			},
			aliases: map[string][]string{"tests": {"integration-test", "unit-test"}},
			want: []Step{
				{Name: "unit-test"},
				{Name: "integration-test"},
				{Name: "build", DependsOn: []string{"integration-test", "unit-test"}},
			},
		},
		{
			name: "no aliases passthrough",
			steps: []Step{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
			},
			aliases: map[string][]string{},
			want: []Step{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
			},
		},
		{
			name: "alias collides with step name",
			steps: []Step{
				{Name: "lint"},
				{Name: "build", DependsOn: []string{"lint"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			wantErr: ErrAliasCollision,
		},
		{
			name: "mixed alias and direct deps",
			steps: []Step{
				{Name: "lint-step"},
				{Name: "unit-test"},
				{Name: "build", DependsOn: []string{"lint", "unit-test"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			want: []Step{
				{Name: "lint-step"},
				{Name: "unit-test"},
				{Name: "build", DependsOn: []string{"lint-step", "unit-test"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ExpandAliases(tt.steps, tt.aliases)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
