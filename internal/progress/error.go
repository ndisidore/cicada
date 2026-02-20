package progress

import "fmt"

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

// NewDisplayErrorf creates a DisplayError with a formatted human message and internal error.
func NewDisplayErrorf(human string, internal error, args ...any) *DisplayError {
	return &DisplayError{Human: fmt.Sprintf(human, args...), Internal: internal}
}
