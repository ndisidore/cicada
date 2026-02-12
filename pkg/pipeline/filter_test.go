package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterJobs(t *testing.T) {
	t.Parallel()

	// Helper to build a minimal single-step job with optional deps and exports.
	job := func(name string, deps []string, exports []Export) Job {
		return Job{
			Name:      name,
			Image:     "alpine",
			DependsOn: deps,
			Exports:   exports,
			Steps: []Step{{
				Name: name,
				Run:  []string{"echo " + name},
			}},
		}
	}

	// Linear chain: lint -> build -> test -> deploy
	linearChain := []Job{
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
	diamondDAG := []Job{
		job("lint", nil, nil),
		job("build", []string{"lint"}, nil),
		job("check", []string{"lint"}, nil),
		job("deploy", []string{"build", "check"}, nil),
	}

	// Branching DAG with exports:
	//   a -> b -> c
	//   d -> e
	branchingDAG := []Job{
		job("a", nil, []Export{{Path: "/out/a", Local: "./a"}}),
		job("b", []string{"a"}, []Export{{Path: "/out/b", Local: "./b"}}),
		job("c", []string{"b"}, []Export{{Path: "/out/c", Local: "./c"}}),
		job("d", nil, []Export{{Path: "/out/d", Local: "./d"}}),
		job("e", []string{"d"}, []Export{{Path: "/out/e", Local: "./e"}}),
	}

	// Multi-step job helper: quality has steps fmt, vet, bench.
	multiStepJob := func(name string, deps []string, stepNames []string, stepExports [][]Export) Job {
		steps := make([]Step, len(stepNames))
		for i, sn := range stepNames {
			steps[i] = Step{Name: sn, Run: []string{"echo " + sn}}
			if stepExports != nil && i < len(stepExports) {
				steps[i].Exports = stepExports[i]
			}
		}
		return Job{
			Name:      name,
			Image:     "alpine",
			DependsOn: deps,
			Steps:     steps,
		}
	}

	// quality (fmt, vet, bench) -> test (unit)
	qualityPipeline := []Job{
		multiStepJob("quality", nil, []string{"fmt", "vet", "bench"}, nil),
		multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
	}

	// quality with step-level exports for testing export stripping.
	qualityWithExports := []Job{
		{
			Name:  "quality",
			Image: "alpine",
			Steps: []Step{
				{Name: "fmt", Run: []string{"echo fmt"}, Exports: []Export{{Path: "/out/fmt", Local: "./fmt"}}},
				{Name: "vet", Run: []string{"echo vet"}, Exports: []Export{{Path: "/out/vet", Local: "./vet"}}},
				{Name: "bench", Run: []string{"echo bench"}, Exports: []Export{{Path: "/out/bench", Local: "./bench"}}},
			},
		},
		multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
	}

	tests := []struct {
		name    string
		jobs    []Job
		opts    FilterOpts
		want    []Job
		wantErr error
	}{
		{
			name: "no flags identity",
			jobs: linearChain,
			opts: FilterOpts{},
			want: linearChain,
		},
		{
			name: "start-at first step is identity",
			jobs: linearChain,
			opts: FilterOpts{StartAt: "lint"},
			want: linearChain,
		},
		{
			name: "start-at mid step linear",
			jobs: linearChain,
			opts: FilterOpts{StartAt: "build"},
			want: []Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
				job("deploy", []string{"test"}, nil),
			},
		},
		{
			name: "start-at last step linear",
			jobs: linearChain,
			opts: FilterOpts{StartAt: "deploy"},
			want: []Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
				job("deploy", []string{"test"}, nil),
			},
		},
		{
			name: "stop-after first step linear",
			jobs: linearChain,
			opts: FilterOpts{StopAfter: "lint"},
			want: []Job{
				job("lint", nil, nil),
			},
		},
		{
			name: "stop-after mid step linear",
			jobs: linearChain,
			opts: FilterOpts{StopAfter: "test"},
			want: []Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
			},
		},
		{
			name: "stop-after last step is identity",
			jobs: linearChain,
			opts: FilterOpts{StopAfter: "deploy"},
			want: linearChain,
		},
		{
			name: "both flags same step",
			jobs: linearChain,
			opts: FilterOpts{StartAt: "build", StopAfter: "build"},
			want: []Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
			},
		},
		{
			name: "both flags window",
			jobs: linearChain,
			opts: FilterOpts{StartAt: "build", StopAfter: "test"},
			want: []Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("test", []string{"build"}, nil),
			},
		},
		{
			name: "diamond start-at one branch includes sibling deps",
			jobs: diamondDAG,
			opts: FilterOpts{StartAt: "build"},
			want: []Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
				job("check", []string{"lint"}, nil),
				job("deploy", []string{"build", "check"}, nil),
			},
		},
		{
			name: "diamond stop-after mid",
			jobs: diamondDAG,
			opts: FilterOpts{StopAfter: "build"},
			want: []Job{
				job("lint", nil, nil),
				job("build", []string{"lint"}, nil),
			},
		},
		{
			name: "diamond start-at deploy includes all deps",
			jobs: diamondDAG,
			opts: FilterOpts{StartAt: "deploy"},
			want: diamondDAG,
		},
		{
			name: "branching start-at only one branch",
			jobs: branchingDAG,
			opts: FilterOpts{StartAt: "d"},
			want: []Job{
				job("d", nil, []Export{{Path: "/out/d", Local: "./d"}}),
				job("e", []string{"d"}, []Export{{Path: "/out/e", Local: "./e"}}),
			},
		},
		{
			name: "branching stop-after only one branch",
			jobs: branchingDAG,
			opts: FilterOpts{StopAfter: "b"},
			want: []Job{
				job("a", nil, []Export{{Path: "/out/a", Local: "./a"}}),
				job("b", []string{"a"}, []Export{{Path: "/out/b", Local: "./b"}}),
			},
		},
		{
			name:    "disjoint branches empty result",
			jobs:    branchingDAG,
			opts:    FilterOpts{StartAt: "d", StopAfter: "b"},
			wantErr: ErrEmptyFilterResult,
		},
		{
			name: "single step pipeline",
			jobs: []Job{
				job("only", nil, nil),
			},
			opts: FilterOpts{StartAt: "only"},
			want: []Job{
				job("only", nil, nil),
			},
		},
		{
			name: "single step stop-after",
			jobs: []Job{
				job("only", nil, nil),
			},
			opts: FilterOpts{StopAfter: "only"},
			want: []Job{
				job("only", nil, nil),
			},
		},
		{
			name: "independent steps start-at",
			jobs: []Job{
				job("a", nil, nil),
				job("b", nil, nil),
				job("c", nil, nil),
			},
			opts: FilterOpts{StartAt: "b"},
			want: []Job{
				job("b", nil, nil),
			},
		},
		{
			name: "independent steps stop-after",
			jobs: []Job{
				job("a", nil, nil),
				job("b", nil, nil),
				job("c", nil, nil),
			},
			opts: FilterOpts{StopAfter: "b"},
			want: []Job{
				job("b", nil, nil),
			},
		},
		{
			name:    "unknown start-at",
			jobs:    linearChain,
			opts:    FilterOpts{StartAt: "nonexistent"},
			wantErr: ErrUnknownStartAt,
		},
		{
			name:    "unknown stop-after",
			jobs:    linearChain,
			opts:    FilterOpts{StopAfter: "nonexistent"},
			wantErr: ErrUnknownStopAfter,
		},
		{
			name: "matrix-expanded names with brackets",
			jobs: []Job{
				job("lint", nil, nil),
				job("build[os=linux]", []string{"lint"}, nil),
				job("build[os=darwin]", []string{"lint"}, nil),
				job("test[os=linux]", []string{"build[os=linux]"}, nil),
				job("test[os=darwin]", []string{"build[os=darwin]"}, nil),
			},
			opts: FilterOpts{StartAt: "build[os=linux]"},
			want: []Job{
				job("lint", nil, nil),
				job("build[os=linux]", []string{"lint"}, nil),
				job("test[os=linux]", []string{"build[os=linux]"}, nil),
			},
		},
		{
			name: "export stripping on dep-only steps",
			jobs: []Job{
				job("a", nil, []Export{{Path: "/out/a", Local: "./a"}}),
				job("b", []string{"a"}, []Export{{Path: "/out/b", Local: "./b"}}),
				job("c", []string{"b"}, []Export{{Path: "/out/c", Local: "./c"}}),
			},
			opts: FilterOpts{StartAt: "b"},
			want: []Job{
				{Name: "a", Image: "alpine", DependsOn: nil, Exports: nil, Steps: []Step{{Name: "a", Run: []string{"echo a"}, Exports: nil}}},
				job("b", []string{"a"}, []Export{{Path: "/out/b", Local: "./b"}}),
				job("c", []string{"b"}, []Export{{Path: "/out/c", Local: "./c"}}),
			},
		},
		{
			name: "export stripping with stop-after",
			jobs: []Job{
				job("a", nil, []Export{{Path: "/out/a", Local: "./a"}}),
				job("b", []string{"a"}, []Export{{Path: "/out/b", Local: "./b"}}),
				job("c", []string{"b"}, []Export{{Path: "/out/c", Local: "./c"}}),
			},
			opts: FilterOpts{StartAt: "c", StopAfter: "c"},
			want: []Job{
				{Name: "a", Image: "alpine", DependsOn: nil, Exports: nil, Steps: []Step{{Name: "a", Run: []string{"echo a"}, Exports: nil}}},
				{Name: "b", Image: "alpine", DependsOn: []string{"a"}, Exports: nil, Steps: []Step{{Name: "b", Run: []string{"echo b"}, Exports: nil}}},
				job("c", []string{"b"}, []Export{{Path: "/out/c", Local: "./c"}}),
			},
		},
		{
			name: "preserves original slice order",
			jobs: []Job{
				job("z", nil, nil),
				job("m", []string{"z"}, nil),
				job("a", []string{"m"}, nil),
			},
			opts: FilterOpts{StopAfter: "m"},
			want: []Job{
				job("z", nil, nil),
				job("m", []string{"z"}, nil),
			},
		},

		// Step-level filtering tests.
		{
			name: "stop-after mid step truncates",
			jobs: qualityPipeline,
			opts: FilterOpts{StopAfter: "quality:fmt"},
			want: []Job{
				multiStepJob("quality", nil, []string{"fmt"}, nil),
			},
		},
		{
			name: "stop-after last step is identity multi-step",
			jobs: qualityPipeline,
			opts: FilterOpts{StopAfter: "quality:bench"},
			want: []Job{
				multiStepJob("quality", nil, []string{"fmt", "vet", "bench"}, nil),
			},
		},
		{
			name: "start-at mid step strips exports from earlier steps",
			jobs: qualityWithExports,
			opts: FilterOpts{StartAt: "quality:vet"},
			want: []Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []Step{
						{Name: "fmt", Run: []string{"echo fmt"}, Exports: nil},
						{Name: "vet", Run: []string{"echo vet"}, Exports: []Export{{Path: "/out/vet", Local: "./vet"}}},
						{Name: "bench", Run: []string{"echo bench"}, Exports: []Export{{Path: "/out/bench", Local: "./bench"}}},
					},
				},
				multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
			},
		},
		{
			name: "start-at last step strips all prior exports",
			jobs: qualityWithExports,
			opts: FilterOpts{StartAt: "quality:bench"},
			want: []Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []Step{
						{Name: "fmt", Run: []string{"echo fmt"}, Exports: nil},
						{Name: "vet", Run: []string{"echo vet"}, Exports: nil},
						{Name: "bench", Run: []string{"echo bench"}, Exports: []Export{{Path: "/out/bench", Local: "./bench"}}},
					},
				},
				multiStepJob("test", []string{"quality"}, []string{"unit"}, nil),
			},
		},
		{
			name: "start-at first step no change",
			jobs: qualityWithExports,
			opts: FilterOpts{StartAt: "quality:fmt"},
			want: qualityWithExports,
		},
		{
			name: "both flags same job step window",
			jobs: qualityWithExports,
			opts: FilterOpts{StartAt: "quality:vet", StopAfter: "quality:bench"},
			want: []Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []Step{
						{Name: "fmt", Run: []string{"echo fmt"}, Exports: nil},
						{Name: "vet", Run: []string{"echo vet"}, Exports: []Export{{Path: "/out/vet", Local: "./vet"}}},
						{Name: "bench", Run: []string{"echo bench"}, Exports: []Export{{Path: "/out/bench", Local: "./bench"}}},
					},
				},
			},
		},
		{
			name: "both flags same job same step",
			jobs: qualityPipeline,
			opts: FilterOpts{StartAt: "quality:vet", StopAfter: "quality:vet"},
			want: []Job{
				multiStepJob("quality", nil, []string{"fmt", "vet"}, nil),
			},
		},
		{
			name:    "both flags same job empty step window",
			jobs:    qualityPipeline,
			opts:    FilterOpts{StartAt: "quality:bench", StopAfter: "quality:fmt"},
			wantErr: ErrEmptyStepWindow,
		},
		{
			name:    "orphaned step in start-at",
			jobs:    qualityPipeline,
			opts:    FilterOpts{StartAt: ":vet"},
			wantErr: ErrOrphanedStep,
		},
		{
			name:    "orphaned step in stop-after",
			jobs:    qualityPipeline,
			opts:    FilterOpts{StopAfter: ":vet"},
			wantErr: ErrOrphanedStep,
		},
		{
			name:    "unknown step in start-at",
			jobs:    qualityPipeline,
			opts:    FilterOpts{StartAt: "quality:nonexistent"},
			wantErr: ErrUnknownStartAtStep,
		},
		{
			name:    "unknown step in stop-after",
			jobs:    qualityPipeline,
			opts:    FilterOpts{StopAfter: "quality:nonexistent"},
			wantErr: ErrUnknownStopAfterStep,
		},
		{
			name: "job:step with job-only on other flag",
			jobs: qualityPipeline,
			opts: FilterOpts{StartAt: "quality:vet", StopAfter: "test"},
			want: []Job{
				{
					Name:  "quality",
					Image: "alpine",
					Steps: []Step{
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
			opts: FilterOpts{StartAt: "quality"},
			want: qualityPipeline,
		},
		{
			name: "colon in matrix name no conflict",
			jobs: []Job{
				job("lint", nil, nil),
				job("build[platform=linux/amd64:latest]", []string{"lint"}, nil),
			},
			opts: FilterOpts{StartAt: "build[platform=linux/amd64:latest]"},
			want: []Job{
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
