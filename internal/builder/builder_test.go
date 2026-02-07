package builder

import (
	"context"
	"testing"

	"github.com/ndisidore/ciro/pkg/pipeline"
)

func TestBuild(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		p         pipeline.Pipeline
		wantSteps int
		wantErr   bool
	}{
		{
			name: "single step",
			p: pipeline.Pipeline{
				Name: "hello",
				Steps: []pipeline.Step{
					{
						Name:  "greet",
						Image: "alpine:latest",
						Run:   []string{"echo hello"},
					},
				},
			},
			wantSteps: 1,
		},
		{
			name: "multi step with dependency",
			p: pipeline.Pipeline{
				Name: "build",
				Steps: []pipeline.Step{
					{
						Name:  "setup",
						Image: "alpine:latest",
						Run:   []string{"echo setup"},
					},
					{
						Name:      "test",
						Image:     "golang:1.23",
						DependsOn: []string{"setup"},
						Run:       []string{"go test ./..."},
					},
				},
			},
			wantSteps: 2,
		},
		{
			name: "step with cache mount",
			p: pipeline.Pipeline{
				Name: "cached",
				Steps: []pipeline.Step{
					{
						Name:  "build",
						Image: "golang:1.23",
						Run:   []string{"go build ./..."},
						Caches: []pipeline.Cache{
							{ID: "go-build", Target: "/root/.cache/go-build"},
						},
					},
				},
			},
			wantSteps: 1,
		},
		{
			name: "step with bind mount",
			p: pipeline.Pipeline{
				Name: "mounted",
				Steps: []pipeline.Step{
					{
						Name:    "build",
						Image:   "rust:1.76",
						Run:     []string{"cargo build"},
						Workdir: "/src",
						Mounts: []pipeline.Mount{
							{Source: ".", Target: "/src"},
						},
					},
				},
			},
			wantSteps: 1,
		},
		{
			name: "multiple run commands joined",
			p: pipeline.Pipeline{
				Name: "multi-cmd",
				Steps: []pipeline.Step{
					{
						Name:  "info",
						Image: "alpine:latest",
						Run:   []string{"uname -a", "date"},
					},
				},
			},
			wantSteps: 1,
		},
		{
			name: "toposort ordering preserved",
			p: pipeline.Pipeline{
				Name: "ordered",
				Steps: []pipeline.Step{
					{
						Name:      "second",
						Image:     "alpine:latest",
						DependsOn: []string{"first"},
						Run:       []string{"echo second"},
					},
					{
						Name:  "first",
						Image: "alpine:latest",
						Run:   []string{"echo first"},
					},
				},
			},
			wantSteps: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := Build(context.Background(), tt.p)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(result.Definitions) != tt.wantSteps {
				t.Errorf(
					"definitions count = %d, want %d",
					len(result.Definitions), tt.wantSteps,
				)
			}
			if len(result.StepNames) != tt.wantSteps {
				t.Errorf(
					"step names count = %d, want %d",
					len(result.StepNames), tt.wantSteps,
				)
			}
			for i, def := range result.Definitions {
				if def == nil {
					t.Errorf("definition[%d] is nil", i)
				}
				if len(def.Def) == 0 {
					t.Errorf("definition[%d] has no operations", i)
				}
			}
		})
	}
}

func TestBuildTopoSortOrder(t *testing.T) {
	t.Parallel()

	p := pipeline.Pipeline{
		Name: "ordered",
		Steps: []pipeline.Step{
			{
				Name:      "c",
				Image:     "alpine:latest",
				DependsOn: []string{"b"},
				Run:       []string{"echo c"},
			},
			{
				Name:      "b",
				Image:     "alpine:latest",
				DependsOn: []string{"a"},
				Run:       []string{"echo b"},
			},
			{
				Name:  "a",
				Image: "alpine:latest",
				Run:   []string{"echo a"},
			},
		},
	}

	result, err := Build(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"a", "b", "c"}
	for i, name := range result.StepNames {
		if name != want[i] {
			t.Errorf("step[%d] = %q, want %q", i, name, want[i])
		}
	}
}
