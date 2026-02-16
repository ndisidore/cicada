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

var (
	_spinnerFrames       = [...]string{"\u280b", "\u2819", "\u2839", "\u2838", "\u283c", "\u2834", "\u2826", "\u2827", "\u2807", "\u280f"} // braille dots
	_boringSpinnerFrames = [...]string{"|", "/", "-", "\\"}
	_spinnerInterval     = 80 * time.Millisecond
)

// stepState tracks a single step's render state.
type stepState struct {
	name     string
	status   stepStatus
	duration time.Duration
}

// _maxLogs caps the number of retained log lines per job.
const _maxLogs = 10

// jobState tracks all vertex and log state for a single job.
type jobState struct {
	vertices     map[digest.Digest]*stepState
	order        []digest.Digest
	logs         []string
	done         bool
	skipped      bool       // true if job was skipped by a when condition
	skippedSteps []string   // step names skipped by static when conditions
	started      *time.Time // earliest vertex start
	ended        *time.Time // latest vertex completion
}

func newJobState() *jobState {
	return &jobState{
		vertices: make(map[digest.Digest]*stepState),
	}
}

func (js *jobState) applyStatus(s *client.SolveStatus) {
	for _, v := range s.Vertexes {
		js.applyVertex(v)
	}
	js.appendLogs(s.Logs)
}

func (js *jobState) applyVertex(v *client.Vertex) {
	st, ok := js.vertices[v.Digest]
	if !ok {
		st = &stepState{name: v.Name, status: statusPending}
		js.vertices[v.Digest] = st
		js.order = append(js.order, v.Digest)
	}

	if st.status == statusDone || st.status == statusCached || st.status == statusError {
		return
	}

	if v.Started != nil && (js.started == nil || v.Started.Before(*js.started)) {
		js.started = v.Started
	}
	if v.Completed != nil && (js.ended == nil || v.Completed.After(*js.ended)) {
		js.ended = v.Completed
	}

	switch {
	case v.Error != "":
		st.status = statusError
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

func (js *jobState) appendLogs(logs []*client.VertexLog) {
	for _, l := range logs {
		if len(l.Data) == 0 {
			continue
		}
		msg := strings.TrimSpace(string(l.Data))
		if msg == "" {
			continue
		}
		js.logs = append(js.logs, msg)
	}
	if len(js.logs) > _maxLogs {
		js.logs = append([]string(nil), js.logs[len(js.logs)-_maxLogs:]...)
	}
}

// multiModel is the bubbletea model for rendering multi-job pipeline progress.
type multiModel struct {
	jobs   map[string]*jobState
	order  []string
	width  int
	boring bool
	done   bool
	frame  int // spinner frame counter
}

// tickMsg drives the spinner animation.
type tickMsg struct{}

// jobAddedMsg signals a new job was attached to the display.
type jobAddedMsg struct{ name string }

// jobStatusMsg carries a SolveStatus for a specific job.
type jobStatusMsg struct {
	name   string
	status *client.SolveStatus
}

// jobDoneMsg signals a job's status channel has been closed.
type jobDoneMsg struct{ name string }

// jobSkippedMsg signals a job was skipped due to a when condition.
type jobSkippedMsg struct{ name string }

// stepSkippedMsg signals a step within a job was skipped due to a when condition.
type stepSkippedMsg struct {
	jobName  string
	stepName string
}

// allDoneMsg signals all jobs have completed and the TUI should quit.
type allDoneMsg struct{}

func newMultiModel(boring bool) *multiModel {
	return &multiModel{
		jobs:   make(map[string]*jobState),
		boring: boring,
	}
}

// Init implements tea.Model.
func (*multiModel) Init() tea.Cmd {
	return tea.Tick(_spinnerInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Update implements tea.Model.
func (m *multiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case jobAddedMsg:
		_ = m.getOrCreateJob(msg.name)
	case jobSkippedMsg:
		js := m.getOrCreateJob(msg.name)
		if !js.done {
			js.done = true
			js.skipped = true
		}
	case stepSkippedMsg:
		// stepSkippedMsg is sent after solveJob returns, which calls Attach
		// (and thus jobAddedMsg) internally, so the job is guaranteed to exist.
		if js, ok := m.jobs[msg.jobName]; ok {
			js.skippedSteps = append(js.skippedSteps, msg.stepName)
		}
	case jobStatusMsg:
		// jobAddedMsg is always sent before the goroutine that produces
		// jobStatusMsg, so the job is guaranteed to exist here.
		if js, ok := m.jobs[msg.name]; ok {
			js.applyStatus(msg.status)
		}
	case jobDoneMsg:
		if js, ok := m.jobs[msg.name]; ok {
			js.done = true
		}
	case allDoneMsg:
		m.done = true
		return m, tea.Quit
	case tickMsg:
		m.frame = (m.frame + 1) % (len(_spinnerFrames) * len(_boringSpinnerFrames))
		return m, tea.Tick(_spinnerInterval, func(time.Time) tea.Msg { return tickMsg{} })
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	default:
	}
	return m, nil
}

func (m *multiModel) getOrCreateJob(name string) *jobState {
	js, ok := m.jobs[name]
	if !ok {
		js = newJobState()
		m.jobs[name] = js
		m.order = append(m.order, name)
	}
	return js
}

var (
	_headerStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")) // bright white
	_logStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))             // dim
	_durationStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))             // cyan
	_stepDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))             // green
	_stepErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))             // red
	_stepRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))             // yellow
	_stepCachedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))             // magenta
	_skipStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))           // gold
)

func statusStyle(s stepStatus) lipgloss.Style {
	switch s {
	case statusDone:
		return _stepDoneStyle
	case statusError:
		return _stepErrorStyle
	case statusRunning:
		return _stepRunningStyle
	case statusCached:
		return _stepCachedStyle
	default:
		return lipgloss.NewStyle()
	}
}

// View implements tea.Model.
func (m *multiModel) View() string {
	var b strings.Builder

	icons := _emojiIcons
	spinnerFrames := _spinnerFrames[:]
	if m.boring {
		icons = _boringIcons
		spinnerFrames = _boringSpinnerFrames[:]
	}

	for i, name := range m.order {
		m.jobs[name].renderTo(&b, name, icons, m.frame, spinnerFrames)
		if i < len(m.order)-1 {
			_ = b.WriteByte('\n')
		}
	}

	return b.String()
}

func (js *jobState) renderTo(b *strings.Builder, name string, icons map[stepStatus]string, frame int, spinnerFrames []string) {
	if js.skipped {
		_, _ = b.WriteString(_skipStyle.Render(fmt.Sprintf("Job: %s (skipped)", name)))
		_ = b.WriteByte('\n')
		return
	}

	resolvedCount := 0
	for _, d := range js.order {
		switch js.vertices[d].status {
		case statusDone, statusCached, statusError:
			resolvedCount++
		default:
		}
	}
	header := fmt.Sprintf("Job: %s (%d/%d)", name, resolvedCount, len(js.order))
	if js.done && js.started != nil && js.ended != nil {
		dur := js.ended.Sub(*js.started).Round(time.Millisecond)
		header += "  " + _durationStyle.Render(dur.String())
	}
	_, _ = b.WriteString(_headerStyle.Render(header))
	_ = b.WriteByte('\n')

	for _, d := range js.order {
		st := js.vertices[d]
		var durStr string
		switch {
		case st.duration > 0:
			durStr = "  " + _durationStyle.Render(st.duration.String())
		case st.status == statusRunning:
			durStr = "  " + _stepRunningStyle.Render(spinnerFrames[frame%len(spinnerFrames)])
		case st.status == statusPending:
			durStr = "  --"
		default:
		}
		styledName := statusStyle(st.status).Render(st.name)
		_, _ = fmt.Fprintf(b, "  %s %s%s\n", icons[st.status], styledName, durStr)
	}

	for _, stepName := range js.skippedSteps {
		_, _ = fmt.Fprintf(b, "  %s\n", _skipStyle.Render(fmt.Sprintf("%s (skipped)", stepName)))
	}

	for _, l := range js.logs {
		_, _ = b.WriteString(_logStyle.Render(fmt.Sprintf("    > %s", l)))
		_ = b.WriteByte('\n')
	}
}
