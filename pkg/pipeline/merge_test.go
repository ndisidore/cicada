package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

func TestMergeJobs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		groups  []pm.JobGroup
		want    []pm.Job
		wantErr error
	}{
		{
			name: "no conflicts",
			groups: []pm.JobGroup{
				{Jobs: []pm.Job{{Name: "a"}}, Origin: "a.kdl"},
				{Jobs: []pm.Job{{Name: "b"}}, Origin: "b.kdl"},
			},
			want: []pm.Job{{Name: "a"}, {Name: "b"}},
		},
		{
			name: "error on duplicate",
			groups: []pm.JobGroup{
				{Jobs: []pm.Job{{Name: "a"}}, Origin: "a.kdl"},
				{Jobs: []pm.Job{{Name: "a"}}, Origin: "b.kdl", OnConflict: pm.ConflictError},
			},
			wantErr: pm.ErrDuplicateJob,
		},
		{
			name: "skip on duplicate",
			groups: []pm.JobGroup{
				{Jobs: []pm.Job{{Name: "a", Image: "first"}}, Origin: "a.kdl"},
				{Jobs: []pm.Job{{Name: "a", Image: "second"}}, Origin: "b.kdl", OnConflict: pm.ConflictSkip},
			},
			want: []pm.Job{{Name: "a", Image: "first"}},
		},
		{
			name: "multiple groups with mixed strategies",
			groups: []pm.JobGroup{
				{Jobs: []pm.Job{{Name: "a"}, {Name: "b"}}, Origin: "inline"},
				{Jobs: []pm.Job{{Name: "c"}, {Name: "a"}}, Origin: "inc.kdl", OnConflict: pm.ConflictSkip},
			},
			want: []pm.Job{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		},
		{
			name:   "empty groups",
			groups: []pm.JobGroup{},
			want:   []pm.Job{},
		},
		{
			name: "ordering preserved across groups",
			groups: []pm.JobGroup{
				{Jobs: []pm.Job{{Name: "c"}, {Name: "a"}}, Origin: "first"},
				{Jobs: []pm.Job{{Name: "b"}, {Name: "d"}}, Origin: "second"},
			},
			want: []pm.Job{{Name: "c"}, {Name: "a"}, {Name: "b"}, {Name: "d"}},
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
		jobs []pm.Job
		want []string
	}{
		{
			name: "single job",
			jobs: []pm.Job{{Name: "a"}},
			want: []string{"a"},
		},
		{
			name: "chain returns last",
			jobs: []pm.Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"b"}},
			},
			want: []string{"c"},
		},
		{
			name: "diamond DAG returns single terminal",
			jobs: []pm.Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
				{Name: "c", DependsOn: []string{"a"}},
				{Name: "d", DependsOn: []string{"b", "c"}},
			},
			want: []string{"d"},
		},
		{
			name: "independent jobs are all terminal",
			jobs: []pm.Job{
				{Name: "a"},
				{Name: "b"},
				{Name: "c"},
			},
			want: []string{"a", "b", "c"},
		},
		{
			name: "multiple terminals in partial DAG",
			jobs: []pm.Job{
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
		jobs    []pm.Job
		aliases map[string][]string
		want    []pm.Job
		wantErr error
	}{
		{
			name: "single alias to single job",
			jobs: []pm.Job{
				{Name: "lint-step"},
				{Name: "build", DependsOn: []string{"lint"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			want: []pm.Job{
				{Name: "lint-step"},
				{Name: "build", DependsOn: []string{"lint-step"}},
			},
		},
		{
			name: "alias to multiple terminal jobs",
			jobs: []pm.Job{
				{Name: "unit-test"},
				{Name: "integration-test"},
				{Name: "build", DependsOn: []string{"tests"}},
			},
			aliases: map[string][]string{"tests": {"integration-test", "unit-test"}},
			want: []pm.Job{
				{Name: "unit-test"},
				{Name: "integration-test"},
				{Name: "build", DependsOn: []string{"integration-test", "unit-test"}},
			},
		},
		{
			name: "no aliases passthrough",
			jobs: []pm.Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
			},
			aliases: map[string][]string{},
			want: []pm.Job{
				{Name: "a"},
				{Name: "b", DependsOn: []string{"a"}},
			},
		},
		{
			name: "alias collides with job name",
			jobs: []pm.Job{
				{Name: "lint"},
				{Name: "build", DependsOn: []string{"lint"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			wantErr: pm.ErrAliasCollision,
		},
		{
			name: "mixed alias and direct deps",
			jobs: []pm.Job{
				{Name: "lint-step"},
				{Name: "unit-test"},
				{Name: "build", DependsOn: []string{"lint", "unit-test"}},
			},
			aliases: map[string][]string{"lint": {"lint-step"}},
			want: []pm.Job{
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
