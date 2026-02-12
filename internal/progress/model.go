package progress

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"

	tea "github.com/charmbracelet/bubbletea"
)

// stepStatus represents the current state of a pipeline vertex.
type stepStatus int

const (
	statusPending stepStatus = iota
	statusRunning
	statusDone
	statusCached
	statusError
)

var _emojiIcons = map[stepStatus]string{
	statusDone:    "\u2705",
	statusRunning: "\U0001f528",
	statusCached:  "\u26a1",
	statusPending: "\u23f3",
	statusError:   "\u274c",
}

var _boringIcons = map[stepStatus]string{
	statusDone:    "[done]  ",
	statusRunning: "[build] ",
	statusCached:  "[cached]",
	statusPending: "[      ]",
	statusError:   "[FAIL]  ",
}

// stepState tracks a single step's render state.
type stepState struct {
	name     string
	status   stepStatus
	duration time.Duration
}

// model is the bubbletea model for rendering pipeline progress.
// All methods use pointer receivers so mutations to the vertices map
// (via applyStatus) operate on the same instance without copy aliasing.
type model struct {
	jobName  string
	vertices map[digest.Digest]*stepState
	order    []digest.Digest // preserves vertex discovery order
	logs     []string        // recent output lines (capped)
	width    int
	boring   bool // use ASCII icons instead of emoji
	done     bool
	err      error
}

// _maxLogs caps the number of retained log lines per model instance.
// appendLogs enforces a sliding window of this size to bound memory
// and keep the TUI viewport readable.
const _maxLogs = 10

func newModel(jobName string, boring bool) *model {
	return &model{
		jobName:  jobName,
		boring:   boring,
		vertices: make(map[digest.Digest]*stepState),
	}
}

// statusMsg carries a SolveStatus from the channel to the bubbletea event loop.
type statusMsg struct{ status *client.SolveStatus }

// doneMsg signals the status channel has been closed.
type doneMsg struct{}

// Init implements tea.Model.
func (*model) Init() tea.Cmd {
	return nil
}

// Update implements tea.Model.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case statusMsg:
		m.applyStatus(msg.status)
	case doneMsg:
		m.done = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *model) applyStatus(s *client.SolveStatus) {
	for _, v := range s.Vertexes {
		st, ok := m.vertices[v.Digest]
		if !ok {
			st = &stepState{name: v.Name, status: statusPending}
			m.vertices[v.Digest] = st
			m.order = append(m.order, v.Digest)
		}

		// Skip updates for vertices already in a terminal state.
		if st.status == statusDone || st.status == statusCached || st.status == statusError {
			continue
		}

		switch {
		case v.Error != "":
			st.status = statusError
			if m.err == nil {
				m.err = fmt.Errorf("vertex %q: %s", v.Name, v.Error)
			}
		case v.Cached:
			st.status = statusCached
		case v.Completed != nil:
			st.status = statusDone
			if v.Started != nil {
				st.duration = v.Completed.Sub(*v.Started).Round(time.Millisecond)
			}
		case v.Started != nil:
			st.status = statusRunning
		default:
		}
	}

	m.appendLogs(s.Logs)
}

func (m *model) appendLogs(logs []*client.VertexLog) {
	for _, l := range logs {
		if len(l.Data) == 0 {
			continue
		}
		msg := strings.TrimSpace(string(l.Data))
		if msg == "" {
			continue
		}
		m.logs = append(m.logs, msg)
		if len(m.logs) > _maxLogs {
			m.logs = m.logs[len(m.logs)-_maxLogs:]
		}
	}
}

var (
	_headerStyle = lipgloss.NewStyle().Bold(true)
	_logStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// View implements tea.Model.
func (m *model) View() string {
	var b strings.Builder

	_, _ = b.WriteString(_headerStyle.Render(fmt.Sprintf("Job: %s", m.jobName)))
	_ = b.WriteByte('\n')

	icons := _emojiIcons
	if m.boring {
		icons = _boringIcons
	}

	for _, d := range m.order {
		st := m.vertices[d]
		icon := icons[st.status]

		durStr := "--"
		if st.duration > 0 {
			durStr = st.duration.String()
		} else if st.status == statusRunning {
			durStr = "..."
		}

		_, _ = fmt.Fprintf(&b, "  %s %s  %s\n", icon, st.name, durStr)
	}

	if len(m.logs) > 0 {
		_ = b.WriteByte('\n')
		for _, l := range m.logs {
			_, _ = b.WriteString(_logStyle.Render(fmt.Sprintf("    > %s", l)))
			_ = b.WriteByte('\n')
		}
	}

	return b.String()
}
