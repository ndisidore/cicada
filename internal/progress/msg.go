package progress

import (
	"log/slog"
	"time"

	"github.com/moby/buildkit/client"
)

// Msg is a sealed interface for all progress display messages.
// Each concrete type satisfies tea.Msg (interface{}) automatically.
type Msg interface {
	progressMsg() // unexported marker; seals the interface to this package
}

// JobAddedMsg signals a new job was registered with the display.
type JobAddedMsg struct {
	Job          string
	StepTimeouts map[string]time.Duration
}

// JobStatusMsg carries a BuildKit SolveStatus update for a specific job.
type JobStatusMsg struct {
	Job    string
	Status *client.SolveStatus
}

// JobDoneMsg signals a job's status channel has been closed.
type JobDoneMsg struct{ Job string }

// JobSkippedMsg signals a job was skipped due to a when condition.
type JobSkippedMsg struct{ Job string }

// StepSkippedMsg signals a step within a job was skipped.
type StepSkippedMsg struct {
	Job  string
	Step string
}

// JobRetryMsg signals a job is being retried after a failure.
type JobRetryMsg struct {
	Job         string
	Attempt     int
	MaxAttempts int
	Err         error
}

// JobTimeoutMsg signals a job exceeded its configured timeout.
type JobTimeoutMsg struct {
	Job     string
	Timeout time.Duration
}

// ErrorMsg carries a display error for real-time rendering.
type ErrorMsg struct {
	Err *DisplayError
}

// LogMsg carries an arbitrary log message for the display.
type LogMsg struct {
	Job     string // optional; empty for pipeline-level messages
	Level   slog.Level
	Message string
}

func (JobAddedMsg) progressMsg()    {}
func (JobStatusMsg) progressMsg()   {}
func (JobDoneMsg) progressMsg()     {}
func (JobSkippedMsg) progressMsg()  {}
func (StepSkippedMsg) progressMsg() {}
func (JobRetryMsg) progressMsg()    {}
func (JobTimeoutMsg) progressMsg()  {}
func (ErrorMsg) progressMsg()       {}
func (LogMsg) progressMsg()         {}
