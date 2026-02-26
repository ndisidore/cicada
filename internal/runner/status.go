package runner

import (
	"context"

	"github.com/moby/buildkit/client"

	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	rm "github.com/ndisidore/cicada/internal/runner/runnermodel"
)

// bridgeStatus returns a closure that forwards JobStatusMsg events from
// displayCh to sender. JobDoneMsg is sent on all exit paths (normal close
// and context cancellation). On context cancellation the bridge drains
// displayCh so the upstream Solve can exit.
// The caller is responsible for running the returned func (e.g. via wg.Go).
func bridgeStatus(ctx context.Context, sender progressmodel.Sender, name string, displayCh <-chan *client.SolveStatus) func() {
	return func() {
		defer func() { sender.Send(progressmodel.JobDoneMsg{Job: name}) }()
		for {
			select {
			case <-ctx.Done():
				//revive:disable-next-line:empty-block // drain so sender can close ch
				for range displayCh {
				}
				return
			case status, ok := <-displayCh:
				if !ok {
					return
				}
				sender.Send(progressmodel.JobStatusMsg{Job: name, Status: status})
			}
		}
	}
}

// drainChannel discards remaining items from ch so the sender is not blocked.
func drainChannel(ch <-chan *client.SolveStatus) {
	//revive:disable-next-line:empty-block // intentionally discarding remaining events
	for range ch {
	}
}

// teeStatusInput groups the non-context parameters for teeStatus (CS-05).
type teeStatusInput struct {
	src       <-chan *client.SolveStatus
	collector *cache.Collector
	observer  rm.StatusObserver
	secrets   map[string]string
	jobName   string
}

// teeStatus interposes a Collector, StatusObserver, and secret redaction
// between the source status channel and the display consumer. If collector,
// observer, and secrets are all nil/empty, returns src directly (zero
// overhead). On context cancellation the goroutine drains src so the Solve
// sender can exit.
//
//revive:disable-next-line:cognitive-complexity teeStatus is a channel-forwarding goroutine; splitting it hurts readability.
func teeStatus(ctx context.Context, in teeStatusInput) <-chan *client.SolveStatus {
	if in.collector == nil && len(in.secrets) == 0 && in.observer == nil {
		return in.src
	}
	if in.observer == nil {
		in.observer = rm.NoopObserver()
	}
	out := make(chan *client.SolveStatus)
	go func() {
		defer in.observer.Flush()
		defer drainChannel(in.src)
		defer close(out)
		for {
			select {
			case status, ok := <-in.src:
				if !ok {
					return
				}
				if status == nil {
					continue
				}
				redactStatus(status, in.secrets)
				if in.collector != nil {
					in.collector.Observe(in.jobName, status)
				}
				in.observer.Observe(status)
				select {
				case out <- status:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
