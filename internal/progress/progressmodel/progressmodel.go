// Package progressmodel provides pure data types and interfaces for progress
// display messaging. Business logic (display adapters, slog handlers) lives
// in the parent progress package and its sub-packages.
package progressmodel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	CmdInfos     map[string]CmdInfo // vertex name -> command metadata
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

// StepRetryMsg signals a step within a job is being retried after a failure.
type StepRetryMsg struct {
	Job         string
	Step        string
	Attempt     int
	MaxAttempts int
	Err         error
}

// StepAllowedFailureMsg signals a step failed but was allowed to fail.
type StepAllowedFailureMsg struct {
	Job  string
	Step string
	Err  error
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

// SyncMsg reports file-sync statistics after context Walk completes.
type SyncMsg struct {
	FilesWalked int64
	FilesHashed int64
	CacheHits   int64
}

func (JobAddedMsg) progressMsg()           {}
func (JobStatusMsg) progressMsg()          {}
func (JobDoneMsg) progressMsg()            {}
func (JobSkippedMsg) progressMsg()         {}
func (StepSkippedMsg) progressMsg()        {}
func (JobRetryMsg) progressMsg()           {}
func (StepRetryMsg) progressMsg()          {}
func (StepAllowedFailureMsg) progressMsg() {}
func (JobTimeoutMsg) progressMsg()         {}
func (ErrorMsg) progressMsg()              {}
func (LogMsg) progressMsg()                {}
func (SyncMsg) progressMsg()               {}

// Sender sends progress messages to a display. Implementations must be
// safe for concurrent use from multiple goroutines.
type Sender interface {
	Send(Msg)
}

// Display manages the lifecycle of a progress display.
type Display interface {
	Sender
	// Start initializes the display and begins consuming messages.
	Start(ctx context.Context) error
	// Shutdown signals no more messages will be sent, waits for all
	// messages to be processed, and returns any display error.
	Shutdown() error
}

// CmdInfo holds the original user command and the full internal command
// for a BuildKit vertex. Populated at build time, carried through the
// pipeline for display layers to show clean user-facing output while
// preserving the full command for debug logging.
type CmdInfo struct {
	UserCmd string // original user command as written in the pipeline
	FullCmd string // full shell command including preamble and retry env prefix
}

// ExitInfo extracts the exit info suffix from a BuildKit process error string.
// Returns the portion after "did not complete successfully: " (e.g. "exit code: 1").
// Returns the full string unchanged if the marker is not found.
func ExitInfo(errStr string) string {
	const marker = "did not complete successfully: "
	if idx := strings.LastIndex(errStr, marker); idx >= 0 {
		return errStr[idx+len(marker):]
	}
	return errStr
}

// DisplayError provides dual human-friendly and internal error representations.
// Human is shown to users by default; Internal carries the full wrapped chain
// for debugging and structured logging.
type DisplayError struct {
	Human    string // user-facing message, shown by default
	Internal error  // full wrapped error chain for debugging/logging
}

func (e *DisplayError) Error() string { return e.Human }
func (e *DisplayError) Unwrap() error { return e.Internal }

// NewDisplayError creates a DisplayError with the given human message and internal error.
func NewDisplayError(human string, internal error) *DisplayError {
	return &DisplayError{Human: human, Internal: internal}
}

// NewDisplayErrorf creates a DisplayError with a formatted human message and
// internal error. The internal error comes first because Go's variadic rules
// require the format args to be last: NewDisplayErrorf(err, "job %q timed out", name).
func NewDisplayErrorf(internal error, human string, args ...any) *DisplayError {
	return &DisplayError{Human: fmt.Sprintf(human, args...), Internal: internal}
}

// IsTimeoutExitCode checks if a BuildKit vertex error indicates a step timeout.
// cfgTimeout must be the timeout parsed from the vertex annotation; when zero
// the function returns false because without a configured timeout the exit
// code is not attributable to the timeout command.
//
// Checked exit codes:
//   - 124: GNU coreutils timeout convention
//   - 137: SIGKILL (128+9); BusyBox timeout with -s KILL exits with the
//     child's signal status. The cfgTimeout guard disambiguates from OOM kills.
func IsTimeoutExitCode(vertexError string, cfgTimeout time.Duration) bool {
	if cfgTimeout == 0 {
		return false
	}
	s := strings.TrimSpace(vertexError)
	return strings.HasSuffix(s, "exit code: 124") ||
		strings.HasSuffix(s, "exit code: 137")
}
