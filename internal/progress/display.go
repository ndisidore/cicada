// Package progress provides display adapters for BuildKit solve progress.
package progress

import "context"

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
