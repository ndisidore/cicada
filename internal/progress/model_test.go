package progress

import (
	"testing"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelUpdate(t *testing.T) {
	t.Parallel()

	now := time.Now()
	completed := now.Add(300 * time.Millisecond)

	tests := []struct {
		name       string
		msgs       []tea.Msg
		wantDone   bool
		wantWidth  int
		wantStatus map[string]stepStatus
	}{
		{
			name: "status message adds vertex",
			msgs: []tea.Msg{
				statusMsg{status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "build", Started: &now},
					},
				}},
			},
			wantStatus: map[string]stepStatus{"build": statusRunning},
		},
		{
			name: "vertex completes",
			msgs: []tea.Msg{
				statusMsg{status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "build", Started: &now, Completed: &completed},
					},
				}},
			},
			wantStatus: map[string]stepStatus{"build": statusDone},
		},
		{
			name: "vertex cached",
			msgs: []tea.Msg{
				statusMsg{status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "lint", Cached: true},
					},
				}},
			},
			wantStatus: map[string]stepStatus{"lint": statusCached},
		},
		{
			name: "vertex error",
			msgs: []tea.Msg{
				statusMsg{status: &client.SolveStatus{
					Vertexes: []*client.Vertex{
						{Digest: digest.FromString("a"), Name: "deploy", Error: "timeout"},
					},
				}},
			},
			wantStatus: map[string]stepStatus{"deploy": statusError},
		},
		{
			name: "done message quits",
			msgs: []tea.Msg{
				doneMsg{},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var result tea.Model = newModel("test", false)

			for _, msg := range tt.msgs {
				var cmd tea.Cmd
				result, cmd = result.Update(msg)
				if tt.wantDone {
					require.NotNil(t, cmd)
				} else {
					assert.Nil(t, cmd)
				}
			}

			rm, ok := result.(*model)
			require.True(t, ok)

			if tt.wantDone {
				assert.True(t, rm.done)
			}

			if tt.wantWidth > 0 {
				assert.Equal(t, tt.wantWidth, rm.width)
			}

			for name, wantSt := range tt.wantStatus {
				var found bool
				for _, st := range rm.vertices {
					if st.name == name {
						assert.Equal(t, wantSt, st.status, "status mismatch for %q", name)
						found = true
						break
					}
				}
				assert.True(t, found, "vertex %q not found", name)
			}
		})
	}
}

func TestModelView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		boring   bool
		setup    func(m *model)
		contains []string
	}{
		{
			name:   "completed step with emoji",
			boring: false,
			setup: func(m *model) {
				d := digest.FromString("v1")
				m.vertices[d] = &stepState{
					name:     "compile",
					status:   statusDone,
					duration: 300 * time.Millisecond,
				}
				m.order = append(m.order, d)
				m.logs = []string{"gcc -o main main.c"}
			},
			contains: []string{"build", "\u2705", "compile", "300ms", "gcc -o main main.c"},
		},
		{
			name:   "completed step boring mode",
			boring: true,
			setup: func(m *model) {
				d := digest.FromString("v1")
				m.vertices[d] = &stepState{
					name:     "compile",
					status:   statusDone,
					duration: 300 * time.Millisecond,
				}
				m.order = append(m.order, d)
			},
			contains: []string{"[done]", "compile", "300ms"},
		},
		{
			name:   "running step shows ellipsis",
			boring: true,
			setup: func(m *model) {
				d := digest.FromString("v2")
				m.vertices[d] = &stepState{
					name:   "lint",
					status: statusRunning,
				}
				m.order = append(m.order, d)
			},
			contains: []string{"[build]", "lint", "..."},
		},
		{
			name:   "pending step shows dashes",
			boring: true,
			setup: func(m *model) {
				d := digest.FromString("v3")
				m.vertices[d] = &stepState{
					name:   "deploy",
					status: statusPending,
				}
				m.order = append(m.order, d)
			},
			contains: []string{"[      ]", "deploy", "--"},
		},
		{
			name:   "running step emoji mode",
			boring: false,
			setup: func(m *model) {
				d := digest.FromString("v2")
				m.vertices[d] = &stepState{
					name:   "lint",
					status: statusRunning,
				}
				m.order = append(m.order, d)
			},
			contains: []string{"\U0001f528", "lint", "..."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m := newModel("build", tt.boring)
			tt.setup(m)

			view := m.View()
			for _, want := range tt.contains {
				assert.Contains(t, view, want)
			}
		})
	}
}

func TestModelLogCapping(t *testing.T) {
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

			m := newModel("test", false)
			logs := make([]*client.VertexLog, tt.logCount)
			for i := range logs {
				logs[i] = &client.VertexLog{Data: []byte("line\n")}
			}

			m.applyStatus(&client.SolveStatus{Logs: logs})
			assert.Len(t, m.logs, tt.wantLen)
		})
	}
}
