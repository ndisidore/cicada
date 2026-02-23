package runner

import (
	"context"

	"github.com/moby/buildkit/client"

	"github.com/ndisidore/cicada/internal/cache"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
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

// teeStatus interposes a Collector and secret redaction between the source
// status channel and the display consumer. If both collector and secrets are
// nil/empty, returns src directly (zero overhead). On context cancellation
// the goroutine drains src so the Solve sender can exit.
//
//revive:disable-next-line:cognitive-complexity teeStatus is a channel-forwarding goroutine; splitting it hurts readability.
func teeStatus(ctx context.Context, src <-chan *client.SolveStatus, collector *cache.Collector, secrets map[string]string, jobName string) <-chan *client.SolveStatus {
	if collector == nil && len(secrets) == 0 {
		return src
	}
	out := make(chan *client.SolveStatus)
	go func() {
		defer drainChannel(src)
		defer close(out)
		for {
			select {
			case status, ok := <-src:
				if !ok {
					return
				}
				redactStatus(status, secrets)
				if collector != nil {
					collector.Observe(jobName, status)
				}
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
