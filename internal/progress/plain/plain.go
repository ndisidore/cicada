// Package plain provides a slog-based text display adapter for BuildKit
// solve progress.
package plain

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"

	"github.com/ndisidore/cicada/internal/progress"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// Compile-time interface check.
var _ progress.Display = (*Display)(nil)

// Display consumes BuildKit status events and emits them as slog messages.
// The slog handler (pretty/json/text) decides how to render.
type Display struct {
	wg sync.WaitGroup
}

// New returns a new plain Display.
func New() *Display {
	return &Display{}
}

// Start is a no-op for Display.
func (*Display) Start(_ context.Context) error { return nil }

// Attach spawns a goroutine that consumes status events and emits slog messages.
func (p *Display) Attach(ctx context.Context, jobName string, ch <-chan *client.SolveStatus, stepTimeouts map[string]time.Duration) error {
	p.wg.Go(func() {
		p.consume(ctx, jobName, ch, stepTimeouts)
	})
	return nil
}

// Skip reports that a job was skipped due to a when condition.
func (*Display) Skip(ctx context.Context, jobName string) {
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelInfo, "job skipped",
		slog.String("job", jobName),
		slog.String("event", "job.skipped"),
	)
}

// SkipStep reports that a step within a job was skipped due to a when condition.
func (*Display) SkipStep(ctx context.Context, jobName, stepName string) {
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelInfo, "step skipped",
		slog.String("job", jobName),
		slog.String("step", stepName),
		slog.String("event", "step.skipped"),
	)
}

// Retry reports that a job is being retried after a failure.
func (*Display) Retry(ctx context.Context, jobName string, attempt, maxAttempts int, err error) {
	errStr := "<nil>"
	if err != nil {
		errStr = err.Error()
	}
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelWarn, "retrying job",
		slog.String("job", jobName),
		slog.Int("attempt", attempt),
		slog.Int("max_attempts", maxAttempts),
		slog.String("error", errStr),
		slog.String("event", "job.retry"),
	)
}

// Timeout reports that a job exceeded its configured timeout.
func (*Display) Timeout(ctx context.Context, jobName string, timeout time.Duration) {
	log := slogctx.FromContext(ctx)
	log.LogAttrs(ctx, slog.LevelWarn, "job timed out",
		slog.String("job", jobName),
		slog.Duration("timeout", timeout),
		slog.String("event", "job.timeout"),
	)
}

// Seal is a no-op for Display; Wait uses WaitGroup which is safe since
// all Attach calls complete before Wait is called by the caller.
func (*Display) Seal() {}

// Wait blocks until all attached jobs complete.
func (p *Display) Wait() error {
	p.wg.Wait()
	return nil
}

// vertexState tracks what has already been printed for each vertex.
type vertexState int

const (
	_stateUnseen vertexState = iota
	_stateStarted
	_stateDone
)

// timeoutVertex tracks a started vertex that has a timeout annotation
// so it can be reported as timed-out if the channel closes before
// BuildKit delivers a terminal status.
type timeoutVertex struct {
	name    string
	timeout time.Duration
}

func (*Display) consume(ctx context.Context, jobName string, ch <-chan *client.SolveStatus, stepTimeouts map[string]time.Duration) {
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
				logPendingTimeouts(ctx, log, jobName, pending)
				return
			}
			for _, v := range status.Vertexes {
				cfgTimeout := stepTimeouts[v.Name]
				// logVertex must run before trackTimeout: logVertex updates
				// seen[v.Digest] which trackTimeout reads to decide whether
				// to add or remove the vertex from pending.
				logVertex(logVertexInput{
					ctx:        ctx,
					log:        log,
					jobName:    jobName,
					v:          v,
					seen:       seen,
					cleanName:  v.Name,
					cfgTimeout: cfgTimeout,
				})
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

// logVertexInput groups parameters for logVertex (CS-05).
type logVertexInput struct {
	ctx        context.Context
	log        *slog.Logger
	jobName    string
	v          *client.Vertex
	seen       map[digest.Digest]vertexState
	cleanName  string
	cfgTimeout time.Duration
}

func logVertex(in logVertexInput) {
	prev := in.seen[in.v.Digest]

	base := []slog.Attr{
		slog.String("job", in.jobName),
		slog.String("vertex", in.cleanName),
	}

	switch {
	case in.v.Error != "":
		if progress.IsTimeoutExitCode(in.v.Error, in.cfgTimeout) {
			attrs := append(base, slog.String("event", "vertex.timeout"), slog.Duration("timeout", in.cfgTimeout))
			//nolint:sloglint // dynamic msg encodes user-facing formatted output
			in.log.LogAttrs(in.ctx, slog.LevelWarn, fmt.Sprintf("[%s] TIMEOUT %s", in.jobName, in.cleanName), attrs...)
		} else {
			attrs := append(base, slog.String("event", "vertex.error"), slog.String("error", in.v.Error))
			//nolint:sloglint // dynamic msg encodes user-facing formatted output
			in.log.LogAttrs(in.ctx, slog.LevelError, fmt.Sprintf("[%s] FAIL %s: %s", in.jobName, in.cleanName, in.v.Error), attrs...)
		}
		in.seen[in.v.Digest] = _stateDone
	case in.v.Cached && prev < _stateDone:
		attrs := append(base, slog.String("event", "vertex.cached"))
		//nolint:sloglint // dynamic msg encodes user-facing formatted output
		in.log.LogAttrs(in.ctx, slog.LevelInfo, fmt.Sprintf("[%s] cached %s", in.jobName, in.cleanName), attrs...)
		in.seen[in.v.Digest] = _stateDone
	case in.v.Completed != nil && prev < _stateDone:
		var dur time.Duration
		if in.v.Started != nil {
			dur = in.v.Completed.Sub(*in.v.Started).Round(time.Millisecond)
		}
		attrs := append(base, slog.String("event", "vertex.done"), slog.Duration("duration", dur))
		//nolint:sloglint // dynamic msg encodes user-facing formatted output
		in.log.LogAttrs(in.ctx, slog.LevelInfo, fmt.Sprintf("[%s] done %s", in.jobName, in.cleanName), attrs...)
		in.seen[in.v.Digest] = _stateDone
	case in.v.Started != nil && prev < _stateStarted:
		attrs := append(base, slog.String("event", "vertex.started"))
		if in.cfgTimeout > 0 {
			attrs = append(attrs, slog.Duration("timeout", in.cfgTimeout))
		}
		//nolint:sloglint // dynamic msg encodes user-facing formatted output
		in.log.LogAttrs(in.ctx, slog.LevelInfo, fmt.Sprintf("[%s] started %s", in.jobName, in.cleanName), attrs...)
		in.seen[in.v.Digest] = _stateStarted
	default:
	}
}

// logPendingTimeouts emits timeout warnings for vertices that never
// reached a terminal state before the channel closed. trackTimeout
// guarantees that pending only contains _stateStarted vertices (entries
// are removed on _stateDone), so no additional state check is needed.
func logPendingTimeouts(ctx context.Context, log *slog.Logger, jobName string, pending map[digest.Digest]timeoutVertex) {
	for _, tv := range pending {
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
