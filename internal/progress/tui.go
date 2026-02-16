package progress

import (
	"context"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/moby/buildkit/client"
)

// TUI renders progress using a bubbletea interactive terminal display.
type TUI struct {
	Boring bool // use ASCII icons instead of emoji

	p           *tea.Program
	wg          sync.WaitGroup
	startOnce   sync.Once
	monitorOnce sync.Once
	sealOnce    sync.Once
	sealCh      chan struct{}
	done        chan struct{}
	mu          sync.Mutex
	err         error
	opts        []tea.ProgramOption // additional program options (for testing)
}

// Start launches the bubbletea program. It is idempotent.
// The bubbletea program runs asynchronously; any runtime errors
// (e.g. terminal initialization failures) are reported via Wait.
func (t *TUI) Start(ctx context.Context) error {
	t.startOnce.Do(func() {
		m := newMultiModel(t.Boring)
		opts := append([]tea.ProgramOption{tea.WithContext(ctx)}, t.opts...)
		p := tea.NewProgram(m, opts...)
		t.p = p
		t.sealCh = make(chan struct{})
		t.done = make(chan struct{})

		go func() {
			_, err := p.Run()
			t.mu.Lock()
			if err != nil {
				t.err = fmt.Errorf("running TUI: %w", err)
			}
			t.mu.Unlock()
			close(t.done)
		}()
	})
	return nil
}

// Attach registers a job's status channel and spawns a consumer goroutine.
// Start must be called before Attach. Concurrent Attach calls are safe, but
// Start and Attach must not race (Start must return before any Attach call).
func (t *TUI) Attach(ctx context.Context, jobName string, ch <-chan *client.SolveStatus) error {
	if t.p == nil {
		return ErrNotStarted
	}
	// Spawn the monitor goroutine on first Attach. It waits for Seal before
	// calling wg.Wait, ensuring all Attach calls (and their wg.Add) have
	// completed before completion is checked.
	t.monitorOnce.Do(func() {
		go func() {
			<-t.sealCh
			t.wg.Wait()
			t.p.Send(allDoneMsg{})
		}()
	})
	t.p.Send(jobAddedMsg{name: jobName})

	t.wg.Go(func() {
		defer func() { t.p.Send(jobDoneMsg{name: jobName}) }()
		for {
			select {
			case <-ctx.Done():
				// Drain so the sender can finish and close ch. Without this,
				// a sender blocked on ch<- can never return to close the channel,
				// leaking its goroutine. Safe because the Solver contract
				// guarantees ch is closed once Solve completes.
				//revive:disable-next-line:empty-block // intentionally draining
				for range ch {
				}
				return
			case status, ok := <-ch:
				if !ok {
					return
				}
				t.p.Send(jobStatusMsg{name: jobName, status: status})
			}
		}
	})

	return nil
}

// Skip reports that a job was skipped due to a when condition.
func (t *TUI) Skip(_ context.Context, jobName string) {
	if t.p != nil {
		t.p.Send(jobSkippedMsg{name: jobName})
	}
}

// SkipStep reports that a step within a job was skipped due to a when condition.
func (t *TUI) SkipStep(_ context.Context, jobName, stepName string) {
	if t.p != nil {
		t.p.Send(stepSkippedMsg{jobName: jobName, stepName: stepName})
	}
}

// Seal signals that no more Attach calls will be made. It is idempotent.
// If no Attach calls occurred, Seal sends allDoneMsg directly so the
// bubbletea program exits and Wait does not hang.
func (t *TUI) Seal() {
	t.sealOnce.Do(func() {
		if t.p == nil {
			return
		}
		// If monitorOnce fires here, no Attach ever ran, so there are
		// no jobs to wait for — send completion immediately.
		t.monitorOnce.Do(func() {
			t.p.Send(allDoneMsg{})
		})
		close(t.sealCh)
	})
}

// Wait blocks until the bubbletea program exits and returns any error.
func (t *TUI) Wait() error {
	if t.done == nil {
		return ErrNotStarted
	}
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}
