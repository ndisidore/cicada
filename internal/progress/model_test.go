package progress

import (
	"fmt"
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tea "github.com/charmbracelet/bubbletea"
)

func TestMultiModelUpdate(t *testing.T) {
	t.Parallel()

	now := time.Now()
	completed := now.Add(300 * time.Millisecond)

	tests := []struct {
		name             string
		msgs             []tea.Msg
		wantDone         bool
		wantWidth        int
		wantJobs         int
		wantSkipped      map[string]bool
		wantSkippedSteps map[string][]string
		wantStatus       map[string]map[string]stepStatus
	}{
		{
			name: "job added then status",
			msgs: []tea.Msg{
				jobAddedMsg{name: "build"},
				jobStatusMsg{name: "build", status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "compile", Started: &now},
					},
				}},
			},
			wantJobs:   1,
			wantStatus: map[string]map[string]stepStatus{"build": {"compile": statusRunning}},
		},
		{
			name: "vertex completes",
			msgs: []tea.Msg{
				jobAddedMsg{name: "lint"},
				jobStatusMsg{name: "lint", status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "golangci", Started: &now, Completed: &completed},
					},
				}},
			},
			wantJobs:   1,
			wantStatus: map[string]map[string]stepStatus{"lint": {"golangci": statusDone}},
		},
		{
			name: "vertex cached",
			msgs: []tea.Msg{
				jobAddedMsg{name: "build"},
				jobStatusMsg{name: "build", status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "deps", Cached: true},
					},
				}},
			},
			wantJobs:   1,
			wantStatus: map[string]map[string]stepStatus{"build": {"deps": statusCached}},
		},
		{
			name: "vertex error",
			msgs: []tea.Msg{
				jobAddedMsg{name: "deploy"},
				jobStatusMsg{name: "deploy", status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "push", Error: "timeout"},
					},
				}},
			},
			wantJobs:   1,
			wantStatus: map[string]map[string]stepStatus{"deploy": {"push": statusError}},
		},
		{
			name: "job skipped",
			msgs: []tea.Msg{
				jobSkippedMsg{name: "deploy"},
			},
			wantJobs:    1,
			wantSkipped: map[string]bool{"deploy": true},
		},
		{
			name: "job added then skipped marks existing job",
			msgs: []tea.Msg{
				jobAddedMsg{name: "deploy"},
				jobSkippedMsg{name: "deploy"},
			},
			wantJobs:    1,
			wantSkipped: map[string]bool{"deploy": true},
		},
		{
			name: "step skipped within job",
			msgs: []tea.Msg{
				jobAddedMsg{name: "build"},
				stepSkippedMsg{jobName: "build", stepName: "slow-test"},
			},
			wantJobs:         1,
			wantSkippedSteps: map[string][]string{"build": {"slow-test"}},
		},
		{
			name: "allDoneMsg quits",
			msgs: []tea.Msg{
				allDoneMsg{},
			},
			wantDone: true,
		},
		{
			name: "window size updates width",
			msgs: []tea.Msg{
				tea.WindowSizeMsg{Width: 120, Height: 40},
			},
			wantWidth: 120,
		},
		{
			name: "multiple jobs",
			msgs: []tea.Msg{
				jobAddedMsg{name: "lint"},
				jobAddedMsg{name: "build"},
				jobStatusMsg{name: "lint", status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "check", Cached: true},
					},
				}},
				jobStatusMsg{name: "build", status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("b"), Name: "compile", Started: &now},
					},
				}},
			},
			wantJobs: 2,
			wantStatus: map[string]map[string]stepStatus{
				"lint":  {"check": statusCached},
				"build": {"compile": statusRunning},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var result tea.Model = newMultiModel(false)

			for _, msg := range tt.msgs {
				var cmd tea.Cmd
				result, cmd = result.Update(msg)
				switch msg.(type) {
				case allDoneMsg:
					require.NotNil(t, cmd)
				case tickMsg:
					require.NotNil(t, cmd)
				default:
					assert.Nil(t, cmd)
				}
			}

			rm, ok := result.(*multiModel)
			require.True(t, ok)

			if tt.wantDone {
				assert.True(t, rm.done)
			}

			if tt.wantWidth > 0 {
				assert.Equal(t, tt.wantWidth, rm.width)
			}

			if tt.wantJobs > 0 {
				assert.Len(t, rm.jobs, tt.wantJobs)
			}

			for jobName, wantSkip := range tt.wantSkipped {
				js, ok := rm.jobs[jobName]
				require.True(t, ok, "job %q not found", jobName)
				assert.Equal(t, wantSkip, js.skipped, "skipped mismatch for %q", jobName)
				assert.True(t, js.done, "skipped job %q should be done", jobName)
			}

			for jobName, wantSteps := range tt.wantSkippedSteps {
				js, ok := rm.jobs[jobName]
				require.True(t, ok, "job %q not found", jobName)
				assert.Equal(t, wantSteps, js.skippedSteps, "skippedSteps mismatch for %q", jobName)
			}

			for jobName, vertices := range tt.wantStatus {
				js, ok := rm.jobs[jobName]
				require.True(t, ok, "job %q not found", jobName)
				for vertexName, wantSt := range vertices {
					var found bool
					for _, st := range js.vertices {
						if st.name == vertexName {
							assert.Equal(t, wantSt, st.status, "status mismatch for %q/%q", jobName, vertexName)
							found = true
							break
						}
					}
					assert.True(t, found, "vertex %q not found in job %q", vertexName, jobName)
				}
			}
		})
	}
}

func TestMultiModelView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		boring   bool
		setup    func(m *multiModel)
		contains []string
	}{
		{
			name:   "completed step with emoji",
			boring: false,
			setup: func(m *multiModel) {
				js := newJobState()
				d := digest.FromString("v1")
				js.vertices[d] = &stepState{
					name:     "compile",
					status:   statusDone,
					duration: 300 * time.Millisecond,
				}
				js.order = append(js.order, d)
				js.logs = []string{"gcc -o main main.c"}
				m.jobs["build"] = js
				m.order = append(m.order, "build")
			},
			contains: []string{"Job: build", "\u2705", "compile", "300ms", "gcc -o main main.c"},
		},
		{
			name:   "completed step boring mode",
			boring: true,
			setup: func(m *multiModel) {
				js := newJobState()
				d := digest.FromString("v1")
				js.vertices[d] = &stepState{
					name:     "compile",
					status:   statusDone,
					duration: 300 * time.Millisecond,
				}
				js.order = append(js.order, d)
				m.jobs["build"] = js
				m.order = append(m.order, "build")
			},
			contains: []string{"[done]", "compile", "300ms"},
		},
		{
			name:   "running step shows spinner",
			boring: true,
			setup: func(m *multiModel) {
				js := newJobState()
				d := digest.FromString("v2")
				js.vertices[d] = &stepState{
					name:   "lint",
					status: statusRunning,
				}
				js.order = append(js.order, d)
				m.jobs["lint"] = js
				m.order = append(m.order, "lint")
			},
			contains: []string{"[build]", "lint", _boringSpinnerFrames[0]},
		},
		{
			name:   "pending step shows dashes",
			boring: true,
			setup: func(m *multiModel) {
				js := newJobState()
				d := digest.FromString("v3")
				js.vertices[d] = &stepState{
					name:   "deploy",
					status: statusPending,
				}
				js.order = append(js.order, d)
				m.jobs["deploy"] = js
				m.order = append(m.order, "deploy")
			},
			contains: []string{"[      ]", "deploy", "--"},
		},
		{
			name:   "error step counts as resolved",
			boring: true,
			setup: func(m *multiModel) {
				js := newJobState()
				d1 := digest.FromString("v1")
				js.vertices[d1] = &stepState{name: "compile", status: statusDone, duration: 100 * time.Millisecond}
				js.order = append(js.order, d1)
				d2 := digest.FromString("v2")
				js.vertices[d2] = &stepState{name: "test", status: statusError}
				js.order = append(js.order, d2)
				d3 := digest.FromString("v3")
				js.vertices[d3] = &stepState{name: "deploy", status: statusPending}
				js.order = append(js.order, d3)
				m.jobs["build"] = js
				m.order = append(m.order, "build")
			},
			contains: []string{"Job: build (2/3)"},
		},
		{
			name:   "skipped job renders header only",
			boring: true,
			setup: func(m *multiModel) {
				js := newJobState()
				js.done = true
				js.skipped = true
				m.jobs["deploy"] = js
				m.order = append(m.order, "deploy")
			},
			contains: []string{"Job: deploy (skipped)"},
		},
		{
			name:   "skipped step renders within job block",
			boring: true,
			setup: func(m *multiModel) {
				js := newJobState()
				d := digest.FromString("v1")
				js.vertices[d] = &stepState{
					name:     "compile",
					status:   statusDone,
					duration: 500 * time.Millisecond,
				}
				js.order = append(js.order, d)
				js.skippedSteps = []string{"notify"}
				m.jobs["deploy"] = js
				m.order = append(m.order, "deploy")
			},
			contains: []string{"Job: deploy", "compile", "notify (skipped)"},
		},
		{
			name:   "multi-job view",
			boring: true,
			setup: func(m *multiModel) {
				js1 := newJobState()
				d1 := digest.FromString("v1")
				js1.vertices[d1] = &stepState{name: "check", status: statusCached}
				js1.order = append(js1.order, d1)
				m.jobs["lint"] = js1
				m.order = append(m.order, "lint")

				js2 := newJobState()
				d2 := digest.FromString("v2")
				js2.vertices[d2] = &stepState{name: "compile", status: statusRunning}
				js2.order = append(js2.order, d2)
				m.jobs["build"] = js2
				m.order = append(m.order, "build")
			},
			contains: []string{"Job: lint", "Job: build", "[cached]", "[build]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := newMultiModel(tt.boring)
			tt.setup(m)

			view := m.View()
			for _, want := range tt.contains {
				assert.Contains(t, view, want)
			}
		})
	}
}

func TestJobStateLogCapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		logCount int
		wantLen  int
	}{
		{
			name:     "under limit keeps all",
			logCount: 5,
			wantLen:  5,
		},
		{
			name:     "over limit caps to max",
			logCount: _maxLogs + 5,
			wantLen:  _maxLogs,
		},
		{
			name:     "at limit keeps all",
			logCount: _maxLogs,
			wantLen:  _maxLogs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			js := newJobState()
			logs := make([]*client.VertexLog, tt.logCount)
			for i := range logs {
				logs[i] = &client.VertexLog{Data: fmt.Appendf(nil, "line %d\n", i)}
			}

			js.applyStatus(&client.SolveStatus{Logs: logs})
			assert.Len(t, js.logs, tt.wantLen)
		})
	}
}
