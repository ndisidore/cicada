package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

func TestFilterJobs(t *testing.T) {
	t.Parallel()

	// Helper to build a minimal single-step job with optional deps and exports.
	job := func(name string, deps []string, exports []pm.Export) pm.Job {
		return pm.Job{
			Name:      name,
			Image:     "alpine",
			DependsOn: deps,
			Exports:   exports,
			Steps: []pm.Step{{
				Name: name,
				Run:  []string{"echo " + name},
			}},
		}
	}

	// Linear chain: lint -> build -> test -> deploy
	linearChain := []pm.Job{
		job("lint", nil, nil),
		job("build", []string{"lint"}, nil),
		job("test", []string{"build"}, nil),
		job("deploy", []string{"test"}, nil),
	}

	// Diamond DAG:
	//   lint
	//  /    \
	// build  check
	//  \    /
	//  deploy
	diamondDAG := []pm.Job{
		job("lint", nil, nil),
		job("build", []string{"lint"}, nil),
		job("check", []string{"lint"}, nil),
		job("deploy", []string{"build", "check"}, nil),
	}

	// Branching DAG with exports:
	//   a -> b -> c
	//   d -> e
	branchingDAG := []pm.Job{
		job("a", nil, []pm.Export{{Path: "/out/a", Local: "./a"}}),
		job("b", []string{"a"}, []pm.Export{{Path: "/out/b", Local: "./b"}}),
		job("c", []string{"b"}, []pm.Export{{Path: "/out/c", Local: "./c"}}),
		job("d", nil, []pm.Export{{Path: "/out/d", Local: "./d"}}),
		job("e", []string{"d"}, []pm.Export{{Path: "/out/e", Local: "./e"}}),
	}

	// Multi-step job helper: quality has steps fmt, vet, bench.
	multiStepJob := func(name string, deps []string, stepNames []string, stepExports [][]pm.Export) pm.Job {
		steps := make([]pm.Step, len(stepNames))
		for i, sn := range stepNames {
			steps[i] = pm.Step{Name: sn, Run: []string{"echo " + sn}}
			if stepExports != nil && i < len(stepExports) {
				steps[i].Exports = stepExports[i]
			}
		}
		return pm.Job{
			Name:      name,
			Image:     "alpine",
			DependsOn: deps,
			Steps:     steps,
		}
	}

	// quality (fmt, vet, bench) -> test (unit)
	qualityPipeline := []pm.Job{
		multiStepJob("quality", nil, []string{"fmt", "vet", "bench"}, nil),
		multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
	}

	// quality with step-level exports for testing export stripping.
	qualityWithExports := []pm.Job{
		{
			Name:  "quality",
			Image: "alpine",
			Steps: []pm.Step{
				{Name: "fmt", Run: []string{"echo fmt"}, Exports: []pm.Export{{Path: "/out/fmt", Local: "./fmt"}}},
				{Name: "vet", Run: []string{"echo vet"}, Exports: []pm.Export{{Path: "/out/vet", Local: "./vet"}}},
				{Name: "bench", Run: []string{"echo bench"}, Exports: []pm.Export{{Path: "/out/bench", Local: "./bench"}}},
			},
		},
		multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
	}

	tests := []struct {
		name    string
		jobs    []pm.Job
		opts    pm.FilterOpts
		want    []pm.Job
		wantErr error
	}{
		{
			name: "no flags identity",
			jobs: linearChain,
			opts: pm.FilterOpts{},
			want: linearChain,
		},
		{
			name: "start-at first step is identity",
			jobs: linearChain,
			opts: pm.FilterOpts{StartAt: "lint"},
			want: linearChain,
		},
		{
			name: "start-at mid step linear",
			jobs: linearChain,
			opts: pm.FilterOpts{StartAt: "build"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
				job("deploy", []string{"test"}, nil),
			},
		},
		{
			name: "start-at last step linear",
			jobs: linearChain,
			opts: pm.FilterOpts{StartAt: "deploy"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
				job("deploy", []string{"test"}, nil),
			},
		},
		{
			name: "stop-after first step linear",
			jobs: linearChain,
			opts: pm.FilterOpts{StopAfter: "lint"},
			want: []pm.Job{
				job("lint", nil, nil),
			},
		},
		{
			name: "stop-after mid step linear",
			jobs: linearChain,
			opts: pm.FilterOpts{StopAfter: "test"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
			},
		},
		{
			name: "stop-after last step is identity",
			jobs: linearChain,
			opts: pm.FilterOpts{StopAfter: "deploy"},
			want: linearChain,
		},
		{
			name: "both flags same step",
			jobs: linearChain,
			opts: pm.FilterOpts{StartAt: "build", StopAfter: "build"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
			},
		},
		{
			name: "both flags window",
			jobs: linearChain,
			opts: pm.FilterOpts{StartAt: "build", StopAfter: "test"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
			},
		},
		{
			name: "diamond start-at one branch includes sibling deps",
			jobs: diamondDAG,
			opts: pm.FilterOpts{StartAt: "build"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("check", []string{"lint"}, nil),
				job("deploy", []string{"build", "check"}, nil),
			},
		},
		{
			name: "diamond stop-after mid",
			jobs: diamondDAG,
			opts: pm.FilterOpts{StopAfter: "build"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
			},
		},
		{
			name: "diamond start-at deploy includes all deps",
			jobs: diamondDAG,
			opts: pm.FilterOpts{StartAt: "deploy"},
			want: diamondDAG,
		},
		{
			name: "branching start-at only one branch",
			jobs: branchingDAG,
			opts: pm.FilterOpts{StartAt: "d"},
			want: []pm.Job{
				job("d", nil, []pm.Export{{Path: "/out/d", Local: "./d"}}),
				job("e", []string{"d"}, []pm.Export{{Path: "/out/e", Local: "./e"}}),
			},
		},
		{
			name: "branching stop-after only one branch",
			jobs: branchingDAG,
			opts: pm.FilterOpts{StopAfter: "b"},
			want: []pm.Job{
				job("a", nil, []pm.Export{{Path: "/out/a", Local: "./a"}}),
				job("b", []string{"a"}, []pm.Export{{Path: "/out/b", Local: "./b"}}),
			},
		},
		{
			name:    "disjoint branches empty result",
			jobs:    branchingDAG,
			opts:    pm.FilterOpts{StartAt: "d", StopAfter: "b"},
			wantErr: pm.ErrEmptyFilterResult,
		},
		{
			name: "single step pipeline",
			jobs: []pm.Job{
				job("only", nil, nil),
			},
			opts: pm.FilterOpts{StartAt: "only"},
			want: []pm.Job{
				job("only", nil, nil),
			},
		},
		{
			name: "single step stop-after",
			jobs: []pm.Job{
				job("only", nil, nil),
			},
			opts: pm.FilterOpts{StopAfter: "only"},
			want: []pm.Job{
				job("only", nil, nil),
			},
		},
		{
			name: "independent steps start-at",
			jobs: []pm.Job{
				job("a", nil, nil),
				job("b", nil, nil),
				job("c", nil, nil),
			},
			opts: pm.FilterOpts{StartAt: "b"},
			want: []pm.Job{
				job("b", nil, nil),
			},
		},
		{
			name: "independent steps stop-after",
			jobs: []pm.Job{
				job("a", nil, nil),
				job("b", nil, nil),
				job("c", nil, nil),
			},
			opts: pm.FilterOpts{StopAfter: "b"},
			want: []pm.Job{
				job("b", nil, nil),
			},
		},
		{
			name:    "unknown start-at",
			jobs:    linearChain,
			opts:    pm.FilterOpts{StartAt: "nonexistent"},
			wantErr: pm.ErrUnknownStartAt,
		},
		{
			name:    "unknown stop-after",
			jobs:    linearChain,
			opts:    pm.FilterOpts{StopAfter: "nonexistent"},
			wantErr: pm.ErrUnknownStopAfter,
		},
		{
			name: "matrix-expanded names with brackets",
			jobs: []pm.Job{
				job("lint", nil, nil),
				job("build[os=linux]", []string{"lint"}, nil),
				job("build[os=darwin]", []string{"lint"}, nil),
				job("test[os=linux]", []string{"build[os=linux]"}, nil),
				job("test[os=darwin]", []string{"build[os=darwin]"}, nil),
			},
			opts: pm.FilterOpts{StartAt: "build[os=linux]"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build[os=linux]", []string{"lint"}, nil),
				job("test[os=linux]", []string{"build[os=linux]"}, nil),
			},
		},
		{
			name: "export stripping on dep-only steps",
			jobs: []pm.Job{
				job("a", nil, []pm.Export{{Path: "/out/a", Local: "./a"}}),
				job("b", []string{"a"}, []pm.Export{{Path: "/out/b", Local: "./b"}}),
				job("c", []string{"b"}, []pm.Export{{Path: "/out/c", Local: "./c"}}),
			},
			opts: pm.FilterOpts{StartAt: "b"},
			want: []pm.Job{
				{Name: "a", Image: "alpine", DependsOn: nil, Exports: nil, Steps: []pm.Step{{Name: "a", Run: []string{"echo a"}, Exports: nil}}},
				job("b", []string{"a"}, []pm.Export{{Path: "/out/b", Local: "./b"}}),
				job("c", []string{"b"}, []pm.Export{{Path: "/out/c", Local: "./c"}}),
			},
		},
		{
			name: "export stripping with stop-after",
			jobs: []pm.Job{
				job("a", nil, []pm.Export{{Path: "/out/a", Local: "./a"}}),
				job("b", []string{"a"}, []pm.Export{{Path: "/out/b", Local: "./b"}}),
				job("c", []string{"b"}, []pm.Export{{Path: "/out/c", Local: "./c"}}),
			},
			opts: pm.FilterOpts{StartAt: "c", StopAfter: "c"},
			want: []pm.Job{
				{Name: "a", Image: "alpine", DependsOn: nil, Exports: nil, Steps: []pm.Step{{Name: "a", Run: []string{"echo a"}, Exports: nil}}},
				{Name: "b", Image: "alpine", DependsOn: []string{"a"}, Exports: nil, Steps: []pm.Step{{Name: "b", Run: []string{"echo b"}, Exports: nil}}},
				job("c", []string{"b"}, []pm.Export{{Path: "/out/c", Local: "./c"}}),
			},
		},
		{
			name: "preserves original slice order",
			jobs: []pm.Job{
				job("z", nil, nil),
				job("m", []string{"z"}, nil),
				job("a", []string{"m"}, nil),
			},
			opts: pm.FilterOpts{StopAfter: "m"},
			want: []pm.Job{
				job("z", nil, nil),
				job("m", []string{"z"}, nil),
			},
		},

		// Step-level filtering tests.
		{
			name: "stop-after mid step truncates",
			jobs: qualityPipeline,
			opts: pm.FilterOpts{StopAfter: "quality:fmt"},
			want: []pm.Job{
				multiStepJob("quality", nil, []string{"fmt"}, nil),
			},
		},
		{
			name: "stop-after last step returns full job",
			jobs: qualityPipeline,
			opts: pm.FilterOpts{StopAfter: "quality:bench"},
			want: []pm.Job{
				multiStepJob("quality", nil, []string{"fmt", "vet", "bench"}, nil),
			},
		},
		{
			name: "start-at mid step strips exports from earlier steps",
			jobs: qualityWithExports,
			opts: pm.FilterOpts{StartAt: "quality:vet"},
			want: []pm.Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []pm.Step{
						{Name: "fmt", Run: []string{"echo fmt"}, Exports: nil},
						{Name: "vet", Run: []string{"echo vet"}, Exports: []pm.Export{{Path: "/out/vet", Local: "./vet"}}},
						{Name: "bench", Run: []string{"echo bench"}, Exports: []pm.Export{{Path: "/out/bench", Local: "./bench"}}},
					},
				},
				multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
			},
		},
		{
			name: "start-at last step strips all prior exports",
			jobs: qualityWithExports,
			opts: pm.FilterOpts{StartAt: "quality:bench"},
			want: []pm.Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []pm.Step{
						{Name: "fmt", Run: []string{"echo fmt"}, Exports: nil},
						{Name: "vet", Run: []string{"echo vet"}, Exports: nil},
						{Name: "bench", Run: []string{"echo bench"}, Exports: []pm.Export{{Path: "/out/bench", Local: "./bench"}}},
					},
				},
				multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
			},
		},
		{
			name: "start-at first step no change",
			jobs: qualityWithExports,
			opts: pm.FilterOpts{StartAt: "quality:fmt"},
			want: qualityWithExports,
		},
		{
			name: "both flags same job step window",
			jobs: qualityWithExports,
			opts: pm.FilterOpts{StartAt: "quality:vet", StopAfter: "quality:bench"},
			want: []pm.Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []pm.Step{
						{Name: "fmt", Run: []string{"echo fmt"}, Exports: nil},
						{Name: "vet", Run: []string{"echo vet"}, Exports: []pm.Export{{Path: "/out/vet", Local: "./vet"}}},
						{Name: "bench", Run: []string{"echo bench"}, Exports: []pm.Export{{Path: "/out/bench", Local: "./bench"}}},
					},
				},
			},
		},
		{
			name: "both flags same job same step",
			jobs: qualityPipeline,
			opts: pm.FilterOpts{StartAt: "quality:vet", StopAfter: "quality:vet"},
			want: []pm.Job{
				multiStepJob("quality", nil, []string{"fmt", "vet"}, nil),
			},
		},
		{
			name:    "both flags same job empty step window",
			jobs:    qualityPipeline,
			opts:    pm.FilterOpts{StartAt: "quality:bench", StopAfter: "quality:fmt"},
			wantErr: pm.ErrEmptyStepWindow,
		},
		{
			name:    "orphaned step in start-at",
			jobs:    qualityPipeline,
			opts:    pm.FilterOpts{StartAt: ":vet"},
			wantErr: pm.ErrOrphanedStep,
		},
		{
			name:    "orphaned step in stop-after",
			jobs:    qualityPipeline,
			opts:    pm.FilterOpts{StopAfter: ":vet"},
			wantErr: pm.ErrOrphanedStep,
		},
		{
			name:    "unknown step in start-at",
			jobs:    qualityPipeline,
			opts:    pm.FilterOpts{StartAt: "quality:nonexistent"},
			wantErr: pm.ErrUnknownStartAtStep,
		},
		{
			name:    "unknown step in stop-after",
			jobs:    qualityPipeline,
			opts:    pm.FilterOpts{StopAfter: "quality:nonexistent"},
			wantErr: pm.ErrUnknownStopAfterStep,
		},
		{
			name: "job:step with job-only on other flag",
			jobs: qualityPipeline,
			opts: pm.FilterOpts{StartAt: "quality:vet", StopAfter: "test"},
			want: []pm.Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []pm.Step{
						{Name: "fmt", Run: []string{"echo fmt"}},
						{Name: "vet", Run: []string{"echo vet"}},
						{Name: "bench", Run: []string{"echo bench"}},
					},
				},
				multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
			},
		},
		{
			name: "job-only still works with multi-step jobs",
			jobs: qualityPipeline,
			opts: pm.FilterOpts{StartAt: "quality"},
			want: qualityPipeline,
		},
		{
			name: "dep-only job has publish stripped",
			jobs: []pm.Job{
				{
					Name:    "a",
					Image:   "alpine",
					Publish: &pm.Publish{Image: "ghcr.io/user/a:v1", Push: true},
					Steps:   []pm.Step{{Name: "a", Run: []string{"echo a"}}},
				},
				job("b", []string{"a"}, nil),
				job("c", []string{"b"}, nil),
			},
			opts: pm.FilterOpts{StartAt: "b"},
			want: []pm.Job{
				{Name: "a", Image: "alpine", DependsOn: nil, Exports: nil, Publish: nil, Steps: []pm.Step{{Name: "a", Run: []string{"echo a"}, Exports: nil}}},
				job("b", []string{"a"}, nil),
				job("c", []string{"b"}, nil),
			},
		},
		{
			name: "colon in matrix name no conflict",
			jobs: []pm.Job{
				job("lint", nil, nil),
				job("build[platform=linux/amd64:latest]", []string{"lint"}, nil),
			},
			opts: pm.FilterOpts{StartAt: "build[platform=linux/amd64:latest]"},
			want: []pm.Job{
				job("lint", nil, nil),
				job("build[platform=linux/amd64:latest]", []string{"lint"}, nil),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FilterJobs(tt.jobs, tt.opts)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseFilterTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want filterTarget
	}{
		{name: "job only", raw: "quality", want: filterTarget{Job: "quality"}},
		{name: "job:step", raw: "quality:vet", want: filterTarget{Job: "quality", Step: "vet"}},
		{name: "empty string", raw: "", want: filterTarget{}},
		{name: "colon in brackets", raw: "build[platform=linux/amd64:latest]", want: filterTarget{Job: "build[platform=linux/amd64:latest]"}},
		{name: "colon after closing bracket", raw: "build[platform=linux/amd64]:mystep", want: filterTarget{Job: "build[platform=linux/amd64]", Step: "mystep"}},
		{name: "colon before brackets", raw: "build:compile[os=linux]", want: filterTarget{Job: "build", Step: "compile[os=linux]"}},
		{name: "colon in brackets and after", raw: "build[platform=linux/amd64:latest]:step", want: filterTarget{Job: "build[platform=linux/amd64:latest]", Step: "step"}},
		{name: "multiple bracket groups with colons", raw: "build[a:b][c:d]:step", want: filterTarget{Job: "build[a:b][c:d]", Step: "step"}},
		{name: "brackets no colon with step", raw: "build[os=linux]:fmt", want: filterTarget{Job: "build[os=linux]", Step: "fmt"}},
		{name: "brackets no colon without step", raw: "build[os=linux]", want: filterTarget{Job: "build[os=linux]"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parseFilterTarget(tt.raw)
			assert.Equal(t, tt.want, got)
		})
	}
}
