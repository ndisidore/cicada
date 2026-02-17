package progress

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"

	"github.com/ndisidore/cicada/pkg/slogctx"
)

// vertexState tracks what has already been printed for each vertex.
type vertexState int

const (
	_stateUnseen vertexState = iota
	_stateStarted
	_stateDone
)

// Plain consumes BuildKit status events and emits them as slog messages.
// The slog handler (pretty/json/text) decides how to render.
type Plain struct {
	wg sync.WaitGroup
}

// Start is a no-op for Plain.
func (*Plain) Start(_ context.Context) error { return nil }

// Attach spawns a goroutine that consumes status events and emits slog messages.
func (p *Plain) Attach(ctx context.Context, jobName string, ch <-chan *client.SolveStatus, stepTimeouts map[string]time.Duration) error {
	p.wg.Go(func() {
		p.consume(ctx, jobName, ch, stepTimeouts)
	})
	return nil
}

// Skip reports that a job was skipped due to a when condition.
func (*Plain) Skip(ctx context.Context, jobName string) {
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelInfo, "job skipped",
		slog.String("job", jobName),
		slog.String("event", "job.skipped"),
	)
}

// SkipStep reports that a step within a job was skipped due to a when condition.
func (*Plain) SkipStep(ctx context.Context, jobName, stepName string) {
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelInfo, "step skipped",
		slog.String("job", jobName),
		slog.String("step", stepName),
		slog.String("event", "step.skipped"),
	)
}

// Retry reports that a job is being retried after a failure.
func (*Plain) Retry(ctx context.Context, jobName string, attempt, maxAttempts int, err error) {
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelWarn, "retrying job",
		slog.String("job", jobName),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.String("error", err.Error()),
		slog.String("event", "job.retry"),
	)
}

// Timeout reports that a job exceeded its configured timeout.
func (*Plain) Timeout(ctx context.Context, jobName string, timeout time.Duration) {
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelWarn, "job timed out",
		slog.String("job", jobName),
		slog.Duration("timeout", timeout),
		slog.String("event", "job.timeout"),
	)
}

// Seal is a no-op for Plain; Wait uses WaitGroup which is safe since
// all Attach calls complete before Wait is called by the caller.
func (*Plain) Seal() {}

// Wait blocks until all attached jobs complete.
func (p *Plain) Wait() error {
	p.wg.Wait()
	return nil
}

// timeoutVertex tracks a started vertex that has a timeout annotation
// so it can be reported as timed-out if the channel closes before
// BuildKit delivers a terminal status.
type timeoutVertex struct {
	name    string
	timeout time.Duration
}

func (*Plain) consume(ctx context.Context, jobName string, ch <-chan *client.SolveStatus, stepTimeouts map[string]time.Duration) {
	log := slogctx.FromContext(ctx)
	seen := make(map[digest.Digest]vertexState)
	pending := make(map[digest.Digest]timeoutVertex)

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
				logPendingTimeouts(ctx, log, jobName, seen, pending)
				return
			}
			for _, v := range status.Vertexes {
				cfgTimeout := stepTimeouts[v.Name]
				logVertex(ctx, log, jobName, v, seen, v.Name, cfgTimeout)
				trackTimeout(v, seen, pending, v.Name, cfgTimeout)
			}
			logLogs(ctx, log, jobName, status.Logs)
		}
	}
}

func logLogs(ctx context.Context, log *slog.Logger, jobName string, logs []*client.VertexLog) {
	for _, l := range logs {
		if len(l.Data) == 0 {
			continue
		}
		msg := strings.TrimSpace(string(l.Data))
		if msg != "" {
			//nolint:sloglint // dynamic msg encodes user-facing formatted output
			log.LogAttrs(ctx, slog.LevelInfo, fmt.Sprintf("[%s] %s", jobName, msg),
				slog.String("event", "output"),
				slog.String("job", jobName),
				slog.String("data", msg),
			)
		}
	}
}

func logVertex(ctx context.Context, log *slog.Logger, jobName string, v *client.Vertex, seen map[digest.Digest]vertexState, cleanName string, cfgTimeout time.Duration) {
	prev := seen[v.Digest]

	base := []slog.Attr{
		slog.String("job", jobName),
		slog.String("vertex", cleanName),
	}

	switch {
	case v.Error != "":
		if isTimeoutExitCode(v.Error, cfgTimeout) {
			attrs := append(base, slog.String("event", "vertex.timeout"), slog.Duration("timeout", cfgTimeout))
			//nolint:sloglint // dynamic msg encodes user-facing formatted output
			log.LogAttrs(ctx, slog.LevelWarn, fmt.Sprintf("[%s] TIMEOUT %s", jobName, cleanName), attrs...)
		} else {
			attrs := append(base, slog.String("event", "vertex.error"), slog.String("error", v.Error))
			//nolint:sloglint // dynamic msg encodes user-facing formatted output
			log.LogAttrs(ctx, slog.LevelError, fmt.Sprintf("[%s] FAIL %s: %s", jobName, cleanName, v.Error), attrs...)
		}
		seen[v.Digest] = _stateDone
	case v.Cached && prev < _stateDone:
		attrs := append(base, slog.String("event", "vertex.cached"))
		//nolint:sloglint // dynamic msg encodes user-facing formatted output
		log.LogAttrs(ctx, slog.LevelInfo, fmt.Sprintf("[%s] cached %s", jobName, cleanName), attrs...)
		seen[v.Digest] = _stateDone
	case v.Completed != nil && prev < _stateDone:
		var dur time.Duration
		if v.Started != nil {
			dur = v.Completed.Sub(*v.Started).Round(time.Millisecond)
		}
		attrs := append(base, slog.String("event", "vertex.done"), slog.Duration("duration", dur))
		//nolint:sloglint // dynamic msg encodes user-facing formatted output
		log.LogAttrs(ctx, slog.LevelInfo, fmt.Sprintf("[%s] done %s", jobName, cleanName), attrs...)
		seen[v.Digest] = _stateDone
	case v.Started != nil && prev < _stateStarted:
		attrs := append(base, slog.String("event", "vertex.started"))
		if cfgTimeout > 0 {
			attrs = append(attrs, slog.Duration("timeout", cfgTimeout))
		}
		//nolint:sloglint // dynamic msg encodes user-facing formatted output
		log.LogAttrs(ctx, slog.LevelInfo, fmt.Sprintf("[%s] started %s", jobName, cleanName), attrs...)
		seen[v.Digest] = _stateStarted
	default:
	}
}

// logPendingTimeouts emits timeout warnings for vertices that started
// but never reached a terminal state before the channel closed.
func logPendingTimeouts(ctx context.Context, log *slog.Logger, jobName string, seen map[digest.Digest]vertexState, pending map[digest.Digest]timeoutVertex) {
	for d, tv := range pending {
		if seen[d] == _stateStarted {
			//nolint:sloglint // dynamic msg encodes user-facing formatted output
			log.LogAttrs(ctx, slog.LevelWarn,
				fmt.Sprintf("[%s] TIMEOUT %s", jobName, tv.name),
				slog.String("job", jobName),
				slog.String("vertex", tv.name),
				slog.String("event", "vertex.timeout"),
				slog.Duration("timeout", tv.timeout),
			)
		}
	}
}

// trackTimeout records or removes timeout-annotated vertices in pending
// based on their current state in seen. cleanName and cfgTimeout are
// pre-parsed by the caller to avoid redundant parseTimeoutAnnotation calls.
func trackTimeout(v *client.Vertex, seen map[digest.Digest]vertexState, pending map[digest.Digest]timeoutVertex, cleanName string, cfgTimeout time.Duration) {
	if cfgTimeout == 0 {
		return
	}
	switch seen[v.Digest] {
	case _stateStarted:
		pending[v.Digest] = timeoutVertex{name: cleanName, timeout: cfgTimeout}
	case _stateDone:
		delete(pending, v.Digest)
	default:
	}
}
