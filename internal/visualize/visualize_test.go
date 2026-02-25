package visualize

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/pkg/conditional"
	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// mkPipeline builds a minimal validated pipeline from the given jobs.
// It performs a simple topological sort inline for test fixtures.
func mkPipeline(name string, jobs []pm.Job) pm.Pipeline {
	// Build name→index map.
	idx := make(map[string]int, len(jobs))
	for i, j := range jobs {
		idx[j.Name] = i
	}
	// Simple DFS topo sort.
	visited := make([]bool, len(jobs))
	order := make([]int, 0, len(jobs))
	var visit func(i int)
	visit = func(i int) {
		if visited[i] {
			return
		}
		visited[i] = true
		for _, dep := range jobs[i].DependsOn {
			di, ok := idx[dep]
			if !ok {
				panic("mkPipeline: job " + jobs[i].Name + " has unknown dependency " + dep)
			}
			visit(di)
		}
		order = append(order, i)
	}
	for i := range len(jobs) {
		visit(i)
	}
	return pm.Pipeline{Name: name, Jobs: jobs, TopoOrder: order}
}

func step(name string) pm.Step {
	return pm.Step{Name: name, Run: []string{"echo " + name}}
}

func TestGraph(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		jobs []pm.Job
		// graph shape assertions
		wantLayerCount int
		wantLayers     [][]int  // nil = skip
		wantDepsCount  int      // number of DependsEdge entries; -1 = skip
		wantNodeMatrix []string // nil = skip; checks g.Nodes[0].Matrix
		wantNodeWhen   string   // non-empty = check g.Nodes[0].When
		wantArtEdge    *Edge    // non-nil = check single ArtifactEdge
		// renderer assertions
		d2Contains   []string // substrings expected in EmitD2 output
		dotContains  []string // substrings expected in EmitDOT output
		termContains []string // substrings expected in RenderTerminal output
	}{
		{
			name: "linear",
			jobs: []pm.Job{
				{Name: "a", Image: "alpine", Steps: []pm.Step{step("s1")}},
				{Name: "b", Image: "alpine", DependsOn: []string{"a"}, Steps: []pm.Step{step("s1")}},
				{Name: "c", Image: "alpine", DependsOn: []string{"b"}, Steps: []pm.Step{step("s1")}},
			},
			wantLayerCount: 3,
			wantLayers:     [][]int{{0}, {1}, {2}},
			wantDepsCount:  2,
		},
		{
			name: "parallel",
			jobs: []pm.Job{
				{Name: "root1", Image: "alpine", Steps: []pm.Step{step("s1")}},
				{Name: "root2", Image: "alpine", Steps: []pm.Step{step("s1")}},
				{Name: "merge", Image: "alpine", DependsOn: []string{"root1", "root2"}, Steps: []pm.Step{step("s1")}},
			},
			wantLayerCount: 2,
			wantLayers:     [][]int{{0, 1}, {2}},
			wantDepsCount:  2,
		},
		{
			name: "matrix",
			jobs: []pm.Job{
				{
					Name:  "build",
					Image: "golang",
					Matrix: &pm.Matrix{
						Dimensions: []pm.Dimension{
							{Name: "go", Values: []string{"1.23", "1.24"}},
							{Name: "platform", Values: []string{"linux/amd64", "linux/arm64"}},
						},
					},
					Steps: []pm.Step{step("compile")},
				},
			},
			wantDepsCount:  -1,
			wantNodeMatrix: []string{"go=1.23,1.24", "platform=linux/amd64,linux/arm64"},
		},
		{
			name: "artifacts",
			jobs: []pm.Job{
				{Name: "build", Image: "golang", Steps: []pm.Step{step("compile")}},
				{
					Name:      "deploy",
					Image:     "alpine",
					DependsOn: []string{"build"},
					Artifacts: []pm.Artifact{{From: "build", Source: "/out/app", Target: "/usr/local/bin/app"}},
					Steps:     []pm.Step{step("push")},
				},
			},
			wantDepsCount: -1,
			wantArtEdge:   &Edge{From: "build", To: "deploy", Label: "/out/app -> /usr/local/bin/app", Kind: ArtifactEdge},
			termContains:  []string{"[artifact:"},
		},
		{
			name: "when_condition",
			jobs: []pm.Job{
				{
					Name:  "conditional",
					Image: "alpine",
					When:  &conditional.When{Expression: "CI == 'true'"},
					Steps: []pm.Step{step("run")},
				},
			},
			wantDepsCount: -1,
			wantNodeWhen:  "CI == 'true'",
		},
		{
			name: "d2_output",
			jobs: []pm.Job{
				{Name: "lint", Image: "alpine", Steps: []pm.Step{step("check")}},
				{Name: "test", Image: "alpine", DependsOn: []string{"lint"}, Steps: []pm.Step{step("unit")}},
			},
			wantDepsCount: -1,
			d2Contains:    []string{"lint:", "test:", "lint -> test"},
		},
		{
			name: "dot_output",
			jobs: []pm.Job{
				{Name: "lint", Image: "alpine", Steps: []pm.Step{step("check")}},
				{Name: "test", Image: "alpine", DependsOn: []string{"lint"}, Steps: []pm.Step{step("unit")}},
			},
			wantDepsCount: -1,
			dotContains:   []string{"digraph pipeline", "subgraph cluster_lint", "subgraph cluster_test", `ltail="cluster_lint"`, `lhead="cluster_test"`},
		},
		{
			name: "terminal_job_names",
			jobs: []pm.Job{
				{Name: "build", Image: "golang", Steps: []pm.Step{step("compile"), step("package")}},
				{Name: "push", Image: "docker", DependsOn: []string{"build"}, Steps: []pm.Step{step("tag")}},
			},
			wantDepsCount: -1,
			termContains:  []string{"build", "push", "compile", "tag"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := mkPipeline(tc.name, tc.jobs)
			g := BuildGraph(p)

			// Graph shape assertions.
			if tc.wantLayerCount > 0 {
				require.Len(t, g.Layers, tc.wantLayerCount)
			}
			for i, want := range tc.wantLayers {
				if i == 0 && len(want) > 1 {
					assert.ElementsMatch(t, want, g.Layers[i])
				} else {
					assert.Equal(t, want, g.Layers[i])
				}
			}
			if tc.wantDepsCount >= 0 {
				var deps []Edge
				for _, e := range g.Edges {
					if e.Kind == DependsEdge {
						deps = append(deps, e)
					}
				}
				assert.Len(t, deps, tc.wantDepsCount)
			}
			if tc.wantNodeMatrix != nil {
				require.Len(t, g.Nodes, 1)
				require.Len(t, g.Nodes[0].Matrix, len(tc.wantNodeMatrix))
				for i, m := range tc.wantNodeMatrix {
					assert.Equal(t, m, g.Nodes[0].Matrix[i])
				}
			}
			if tc.wantNodeWhen != "" {
				require.Len(t, g.Nodes, 1)
				assert.Equal(t, tc.wantNodeWhen, g.Nodes[0].When)
			}
			if tc.wantArtEdge != nil {
				var artEdges []Edge
				for _, e := range g.Edges {
					if e.Kind == ArtifactEdge {
						artEdges = append(artEdges, e)
					}
				}
				require.Len(t, artEdges, 1)
				assert.Equal(t, tc.wantArtEdge.From, artEdges[0].From)
				assert.Equal(t, tc.wantArtEdge.To, artEdges[0].To)
				assert.Equal(t, tc.wantArtEdge.Label, artEdges[0].Label)
			}

			// Renderer assertions.
			if len(tc.d2Contains) > 0 {
				t.Run("d2", func(t *testing.T) {
					t.Parallel()
					var buf bytes.Buffer
					require.NoError(t, EmitD2(g, &buf))
					out := buf.String()
					for _, s := range tc.d2Contains {
						assert.Contains(t, out, s)
					}
				})
			}
			if len(tc.dotContains) > 0 {
				t.Run("dot", func(t *testing.T) {
					t.Parallel()
					var buf bytes.Buffer
					require.NoError(t, EmitDOT(g, &buf))
					out := buf.String()
					for _, s := range tc.dotContains {
						assert.Contains(t, out, s)
					}
				})
			}
			if len(tc.termContains) > 0 {
				t.Run("terminal", func(t *testing.T) {
					t.Parallel()
					var buf bytes.Buffer
					require.NoError(t, RenderTerminal(g, &buf))
					out := buf.String()
					for _, s := range tc.termContains {
						assert.Contains(t, out, s)
					}
				})
			}
		})
	}
}

func TestEmitToFile_dispatch(t *testing.T) {
	t.Parallel()

	jobs := []pm.Job{
		{Name: "a", Image: "alpine", Steps: []pm.Step{step("s1")}},
		{Name: "b", Image: "alpine", DependsOn: []string{"a"}, Steps: []pm.Step{step("s2")}},
	}
	g := BuildGraph(mkPipeline("dispatch", jobs))

	tests := []struct {
		ext      string
		contains string
	}{
		{".d2", "a ->"},
		{".dot", "digraph pipeline"},
	}
	for _, tc := range tests {
		t.Run(tc.ext, func(t *testing.T) {
			t.Parallel()
			var direct, viaFile bytes.Buffer
			switch tc.ext {
			case ".d2":
				require.NoError(t, EmitD2(g, &direct))
				require.NoError(t, EmitToFile(g, "out"+tc.ext, &viaFile))
			case ".dot":
				require.NoError(t, EmitDOT(g, &direct))
				require.NoError(t, EmitToFile(g, "out"+tc.ext, &viaFile))
			default:
				t.Fatalf("unhandled ext %s", tc.ext)
			}
			assert.Equal(t, direct.String(), viaFile.String())
			assert.Contains(t, viaFile.String(), tc.contains)
		})
	}
}

func TestEmitToFile_unsupportedExt(t *testing.T) {
	t.Parallel()

	g := BuildGraph(mkPipeline("p", []pm.Job{{Name: "a", Image: "alpine", Steps: []pm.Step{step("s")}}}))
	var buf bytes.Buffer
	err := EmitToFile(g, "out.svg", &buf)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported output format")
}

func TestRenderers_emptyGraph(t *testing.T) {
	t.Parallel()

	g := BuildGraph(mkPipeline("empty", nil))

	t.Run("d2", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		require.NoError(t, EmitD2(g, &buf))
	})
	t.Run("dot", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		require.NoError(t, EmitDOT(g, &buf))
	})
	t.Run("terminal", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		require.NoError(t, RenderTerminal(g, &buf))
	})
	t.Run("emitToFile_d2", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		require.NoError(t, EmitToFile(g, "out.d2", &buf))
	})
}
