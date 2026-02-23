package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
)

// stepStatus represents the current state of a pipeline vertex.
type stepStatus int

const (
	statusPending stepStatus = iota
	statusRunning
	statusDone
	statusCached
	statusError
	statusTimeout
	statusRetry
	statusAllowedFailure
)

// TUI status icons use unicode/emoji for the interactive terminal display.
//
//codecheck:allow-emojis // CS-07 exception (2): TUI-only visual indicators.
var _richIcons = map[stepStatus]string{
	statusDone:           "\u2705",
	statusRunning:        "\U0001f528",
	statusCached:         "\u26a1",
	statusPending:        "\u23f3",
	statusError:          "\U0001f6a8",
	statusTimeout:        "\u23f0",
	statusRetry:          "\U0001f504",
	statusAllowedFailure: "\u26a0\ufe0f",
}

var _boringIcons = map[stepStatus]string{
	statusDone:           "[+] ",
	statusRunning:        "[~] ",
	statusCached:         "[=] ",
	statusPending:        "[ ] ",
	statusError:          "[!] ",
	statusTimeout:        "[t] ",
	statusRetry:          "[r] ",
	statusAllowedFailure: "[w] ",
}

var (
	// braille dots — TUI-only visual indicator
	_spinnerFrames       = [...]string{"\u280b", "\u2819", "\u2839", "\u2838", "\u283c", "\u2834", "\u2826", "\u2827", "\u2807", "\u280f"}
	_boringSpinnerFrames = [...]string{"|", "/", "-", "\\"}
	_spinnerInterval     = 80 * time.Millisecond
)

// stepState tracks a single step's render state.
type stepState struct {
	name              string
	status            stepStatus
	duration          time.Duration
	configuredTimeout time.Duration // parsed from vertex name annotation; zero means no timeout
}

// _maxLogs caps the number of retained log lines per job.
const _maxLogs = 10

// jobState tracks all vertex and log state for a single job.
type jobState struct {
	vertices          map[digest.Digest]*stepState
	order             []digest.Digest
	logs              []string
	done              bool
	skipped           bool                             // true if job was skipped by a when condition
	skippedSteps      []string                         // step names skipped by static when conditions
	started           *time.Time                       // earliest vertex start
	ended             *time.Time                       // latest vertex completion
	retryAttempt      int                              // current retry attempt (0 = no retry)
	maxAttempts       int                              // total max attempts
	timedOut          bool                             // whether the job timed out
	timeout           time.Duration                    // the configured timeout
	stepTimeouts      map[string]time.Duration         // vertex name -> configured step timeout
	cmdInfos          map[string]progressmodel.CmdInfo // vertex name -> command metadata
	allowedFailureSet map[string]struct{}              // step name prefixes whose failures are allowed
}

// jobDuration returns the wall-clock duration for a completed job. For
// timed-out jobs this is the configured timeout (the running step never
// receives a Completed timestamp from BuildKit). For normal jobs it is
// the span from the earliest start to the latest completion.
func (js *jobState) jobDuration() time.Duration {
	if js.timedOut {
		return js.timeout
	}
	if js.ended != nil && js.started != nil {
		return js.ended.Sub(*js.started).Round(time.Millisecond)
	}
	return 0
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

//revive:disable-next-line:cyclomatic applyVertex is a linear status state machine; splitting it hurts readability.
func (js *jobState) applyVertex(v *client.Vertex) {
	st, ok := js.vertices[v.Digest]
	if !ok {
		cfgTimeout := js.stepTimeouts[v.Name]
		st = &stepState{name: v.Name, status: statusPending, configuredTimeout: cfgTimeout}
		js.vertices[v.Digest] = st
		js.order = append(js.order, v.Digest)
	}

	if st.status == statusDone || st.status == statusCached || st.status == statusError || st.status == statusTimeout || st.status == statusAllowedFailure {
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
		if progressmodel.IsTimeoutExitCode(v.Error, st.configuredTimeout) {
			st.status = statusTimeout
		} else {
			st.status = statusError
		}
		if js.isAllowedFailure(st.name) {
			st.status = statusAllowedFailure
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

// isAllowedFailure reports whether the vertex name matches any allowed-failure
// step prefix, enabling order-independent promotion regardless of message arrival.
func (js *jobState) isAllowedFailure(vertexName string) bool {
	for prefix := range js.allowedFailureSet {
		if strings.HasPrefix(vertexName, prefix) {
			return true
		}
	}
	return false
}

// addLog appends a single line to the job's log buffer and enforces _maxLogs.
func (js *jobState) addLog(line string) {
	js.logs = append(js.logs, line)
	if len(js.logs) > _maxLogs {
		js.logs = append([]string(nil), js.logs[len(js.logs)-_maxLogs:]...)
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
		js.addLog(msg)
	}
}

// multiModel is the bubbletea model for rendering multi-job pipeline progress.
type multiModel struct {
	jobs    map[string]*jobState
	order   []string
	width   int
	boring  bool
	done    bool
	frame   int // spinner frame counter
	syncMsg *progressmodel.SyncMsg
}

// tickMsg drives the spinner animation (TUI-internal lifecycle).
type tickMsg struct{}

// allDoneMsg signals all jobs have completed and the TUI should quit (TUI-internal lifecycle).
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
//
//revive:disable-next-line:cyclomatic,cognitive-complexity,function-length Update is a flat message dispatch; splitting it hurts readability.
func (m *multiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case progressmodel.JobAddedMsg:
		js := m.getOrCreateJob(msg.Job)
		if msg.StepTimeouts != nil {
			js.stepTimeouts = msg.StepTimeouts
		}
		js.cmdInfos = msg.CmdInfos
	case progressmodel.JobSkippedMsg:
		js := m.getOrCreateJob(msg.Job)
		if !js.done {
			js.done = true
			js.skipped = true
		}
	case progressmodel.StepSkippedMsg:
		// StepSkippedMsg is sent after solveJob returns, which sends
		// JobAddedMsg first, so the job is guaranteed to exist.
		if js, ok := m.jobs[msg.Job]; ok {
			js.skippedSteps = append(js.skippedSteps, msg.Step)
		}
	case progressmodel.JobStatusMsg:
		// JobAddedMsg is always sent before the goroutine that produces
		// JobStatusMsg, so the job is guaranteed to exist here.
		if js, ok := m.jobs[msg.Job]; ok {
			js.applyStatus(msg.Status)
		}
	case progressmodel.JobRetryMsg:
		js := m.getOrCreateJob(msg.Job)
		js.retryAttempt = msg.Attempt
		js.maxAttempts = msg.MaxAttempts
	case progressmodel.StepRetryMsg:
		if js, ok := m.jobs[msg.Job]; ok {
			js.addLog(fmt.Sprintf("step %q: retrying (attempt %d/%d)", msg.Step, msg.Attempt, msg.MaxAttempts))
		}
	case progressmodel.StepAllowedFailureMsg:
		if js, ok := m.jobs[msg.Job]; ok {
			js.addLog(fmt.Sprintf("step %q: failed (allowed)", msg.Step))
			prefix := msg.Job + "/" + msg.Step + "/"
			if js.allowedFailureSet == nil {
				js.allowedFailureSet = make(map[string]struct{})
			}
			js.allowedFailureSet[prefix] = struct{}{}
			// Retrofit any vertices already in error/timeout state.
			for _, st := range js.vertices {
				if (st.status == statusError || st.status == statusTimeout) && strings.HasPrefix(st.name, prefix) {
					st.status = statusAllowedFailure
				}
			}
		}
	case progressmodel.JobTimeoutMsg:
		js := m.getOrCreateJob(msg.Job)
		js.timedOut = true
		js.timeout = msg.Timeout
		for _, d := range js.order {
			st := js.vertices[d]
			if st.status == statusRunning {
				st.status = statusTimeout
			}
		}
	case progressmodel.SyncMsg:
		m.syncMsg = &msg
	case progressmodel.JobDoneMsg:
		if js, ok := m.jobs[msg.Job]; ok {
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
	_headerStyle          = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")) // bright white
	_logStyle             = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))             // dim
	_durationStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))             // cyan
	_stepDoneStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))             // green
	_stepErrorStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))             // red
	_stepRunningStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))             // blue
	_stepCachedStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))             // magenta
	_stepTimeoutStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("208"))           // orange
	_stepRetryStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))           // gold
	_stepAllowedFailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))            // bright yellow
	_skipStyle            = lipgloss.NewStyle().Foreground(lipgloss.Color("178"))           // gold
)

func statusStyle(s stepStatus) lipgloss.Style {
	switch s {
	case statusDone:
		return _stepDoneStyle
	case statusError:
		return _stepErrorStyle
	case statusTimeout:
		return _stepTimeoutStyle
	case statusRunning:
		return _stepRunningStyle
	case statusCached:
		return _stepCachedStyle
	case statusRetry:
		return _stepRetryStyle
	case statusAllowedFailure:
		return _stepAllowedFailStyle
	default:
		return lipgloss.NewStyle()
	}
}

// View implements tea.Model.
func (m *multiModel) View() string {
	var b strings.Builder

	icons := _richIcons
	spinnerFrames := _spinnerFrames[:]
	if m.boring {
		icons = _boringIcons
		spinnerFrames = _boringSpinnerFrames[:]
	}

	if m.syncMsg != nil {
		_, _ = fmt.Fprintf(&b, "%s  files=%d hashed=%d cached=%d\n",
			_logStyle.Render("sync:"), m.syncMsg.FilesWalked, m.syncMsg.FilesHashed, m.syncMsg.CacheHits)
	}

	rc := renderCtx{
		icons:         icons,
		frame:         m.frame,
		spinnerFrames: spinnerFrames,
	}
	for i, name := range m.order {
		m.jobs[name].renderTo(&b, name, rc)
		if i < len(m.order)-1 {
			_ = b.WriteByte('\n')
		}
	}

	return b.String()
}

// renderCtx groups per-render parameters to keep renderTo under CS-05.
type renderCtx struct {
	icons         map[stepStatus]string
	frame         int
	spinnerFrames []string
}

//revive:disable-next-line:cyclomatic renderTo is a linear rendering pipeline.
func (js *jobState) renderTo(b *strings.Builder, name string, rc renderCtx) {
	if js.skipped {
		_, _ = b.WriteString(_skipStyle.Render(fmt.Sprintf("Job: %s (skipped)", name)))
		_ = b.WriteByte('\n')
		return
	}

	resolvedCount := 0
	for _, d := range js.order {
		switch js.vertices[d].status {
		case statusDone, statusCached, statusError, statusTimeout, statusAllowedFailure:
			resolvedCount++
		default:
		}
	}
	header := fmt.Sprintf("Job: %s (%d/%d)", name, resolvedCount, len(js.order))
	if js.done && js.started != nil {
		dur := js.jobDuration()
		if dur > 0 {
			header += "  " + _durationStyle.Render(dur.String())
		}
	}
	if js.retryAttempt > 0 {
		header += fmt.Sprintf("  %s attempt %d/%d", rc.icons[statusRetry], js.retryAttempt, js.maxAttempts)
	}
	if js.timedOut {
		header += fmt.Sprintf("  %s timed out (%s)", rc.icons[statusTimeout], js.timeout)
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
			durStr = "  " + _stepRunningStyle.Render(rc.spinnerFrames[rc.frame%len(rc.spinnerFrames)])
		case st.status == statusPending:
			durStr = "  --"
		default:
		}
		styledName := statusStyle(st.status).Render(st.name)
		_, _ = fmt.Fprintf(b, "  %s %s%s\n", rc.icons[st.status], styledName, durStr)
	}

	for _, stepName := range js.skippedSteps {
		_, _ = fmt.Fprintf(b, "  %s\n", _skipStyle.Render(fmt.Sprintf("%s (skipped)", stepName)))
	}

	for _, l := range js.logs {
		_, _ = b.WriteString(_logStyle.Render(fmt.Sprintf("    > %s", l)))
		_ = b.WriteByte('\n')
	}
}
