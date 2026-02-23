package runnermodel

import (
	"errors"
	"fmt"
	"time"
)

// ErrNilSolver indicates that Solver was not provided.
var ErrNilSolver = errors.New("solver must not be nil")

// ErrNilSender indicates that Sender was not provided.
var ErrNilSender = errors.New("sender must not be nil")

// ErrNilDefinition indicates that an LLB Definition is unexpectedly nil.
var ErrNilDefinition = errors.New("definition must not be nil")

// ErrNilRuntime indicates that Runtime was not provided but export-docker requires it.
var ErrNilRuntime = errors.New("runtime must not be nil for export-docker")

// ErrJobTimeout indicates a job exceeded its configured timeout.
var ErrJobTimeout = errors.New("job timeout")

// ErrStepTimeout indicates a step exceeded its configured timeout.
var ErrStepTimeout = errors.New("step timeout")

// JobTimeoutError provides structured detail about a job timeout.
type JobTimeoutError struct {
	JobName string
	Timeout time.Duration
}

func (e *JobTimeoutError) Error() string {
	return fmt.Sprintf("job %q exceeded %s timeout", e.JobName, e.Timeout)
}

func (*JobTimeoutError) Unwrap() error { return ErrJobTimeout }

// StepTimeoutError provides structured detail about a step timeout.
type StepTimeoutError struct {
	JobName  string
	StepName string
	Timeout  time.Duration
}

func (e *StepTimeoutError) Error() string {
	return fmt.Sprintf("step %q in job %q exceeded %s timeout", e.StepName, e.JobName, e.Timeout)
}

func (*StepTimeoutError) Unwrap() error { return ErrStepTimeout }

// ErrExportDockerMultiPlatform indicates export-docker was set on a multi-platform publish.
var ErrExportDockerMultiPlatform = errors.New("export-docker is not supported for multi-platform publishes")

// ErrPublishSettingConflict indicates variants targeting the same image have inconsistent settings.
var ErrPublishSettingConflict = errors.New("conflicting publish settings for same image")

// ErrDuplicatePlatform indicates two variants in a multi-platform publish target the same platform.
var ErrDuplicatePlatform = errors.New("duplicate platform in multi-platform publish")
