package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeJobs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		groups  []JobGroup
		want    []Job
		wantErr error
	}{
		{
			name: "no conflicts",
			groups: []JobGroup{
				{Jobs: []Job{{Name: "a"}}, Origin: "a.kdl"},
				{Jobs: []Job{{Name: "b"}}, Origin: "b.kdl"},
			},
			want: []Job{{Name: "a"}, {Name: "b"}},
		},
		{
			name: "error on duplicate",
			groups: []JobGroup{
				{Jobs: []Job{{Name: "a"}}, Origin: "a.kdl"},
				{Jobs: []Job{{Name: "a"}}, Origin: "b.kdl", OnConflict: ConflictError},
			},
			wantErr: ErrDuplicateJob,
		},
		{
			name: "skip on duplicate",
			groups: []JobGroup{
				{Jobs: []Job{{Name: "a", Image: "first"}}, Origin: "a.kdl"},
				{Jobs: []Job{{Name: "a", Image: "second"}}, Origin: "b.kdl", OnConflict: ConflictSkip},
			},
			want: []Job{{Name: "a", Image: "first"}},
		},
		{
			name: "multiple groups with mixed strategies",
			groups: []JobGroup{
				{Jobs: []Job{{Name: "a"}, {Name: "b"}}, Origin: "inline"},
				{Jobs: []Job{{Name: "c"}, {Name: "a"}}, Origin: "inc.kdl", OnConflict: ConflictSkip},
			},
			want: []Job{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		},
		{
			name:   "empty groups",
			groups: []JobGroup{},
			want:   []Job{},
		},
		{
			name: "ordering preserved across groups",
			groups: []JobGroup{
				{Jobs: []Job{{Name: "c"}, {Name: "a"}}, Origin: "first"},
				{Jobs: []Job{{Name: "b"}, {Name: "d"}}, Origin: "second"},
			},
			want: []Job{{Name: "c"}, {Name: "a"}, {Name: "b"}, {Name: "d"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MergeJobs(tt.groups)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTerminalJobs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		jobs []Job
		want []string
	}{
		{
			name: "single job",
			jobs: []Job{{Name: "a"}},
			want: []string{"a"},
		},
		{
			name: "chain returns last",
			jobs: []Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"b"}},
			},
			want: []string{"c"},
		},
		{
			name: "diamond DAG returns single terminal",
			jobs: []Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"a"}},
				{Name: "d", DependsOn: []string{"b", "c"}},
			},
			want: []string{"d"},
		},
		{
			name: "independent jobs are all terminal",
			jobs: []Job{
				{Name: "a"},
				{Name: "b"},
				{Name: "c"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "multiple terminals in partial DAG",
			jobs: []Job{
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
			got := TerminalJobs(tt.jobs)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		jobs    []Job
		aliases map[string][]string
		want    []Job
		wantErr error
	}{
		{
			name: "single alias to single job",
			jobs: []Job{
				{Name: "lint-step"},
				{Name: "build", DependsOn: []string{"lint"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			want: []Job{
				{Name: "lint-step"},
				{Name: "build", DependsOn: []string{"lint-step"}},
			},
		},
		{
			name: "alias to multiple terminal jobs",
			jobs: []Job{
				{Name: "unit-test"},
				{Name: "integration-test"},
				{Name: "build", DependsOn: []string{"tests"}},
			},
			aliases: map[string][]string{"tests": {"integration-test", "unit-test"}},
			want: []Job{
				{Name: "unit-test"},
				{Name: "integration-test"},
				{Name: "build", DependsOn: []string{"integration-test", "unit-test"}},
			},
		},
		{
			name: "no aliases passthrough",
			jobs: []Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
			},
			aliases: map[string][]string{},
			want: []Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
			},
		},
		{
			name: "alias collides with job name",
			jobs: []Job{
				{Name: "lint"},
				{Name: "build", DependsOn: []string{"lint"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			wantErr: ErrAliasCollision,
		},
		{
			name: "mixed alias and direct deps",
			jobs: []Job{
				{Name: "lint-step"},
				{Name: "unit-test"},
				{Name: "build", DependsOn: []string{"lint", "unit-test"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			want: []Job{
				{Name: "lint-step"},
				{Name: "unit-test"},
				{Name: "build", DependsOn: []string{"lint-step", "unit-test"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ExpandAliases(tt.jobs, tt.aliases)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
