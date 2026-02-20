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

// Display consumes progress messages and emits them as slog messages.
// The slog handler (pretty/json/text) decides how to render.
type Display struct {
	ctx          context.Context
	ch           chan progress.Msg
	wg           sync.WaitGroup
	once         sync.Once
	shutdownOnce sync.Once
}

// New returns a new plain Display.
func New() *Display {
	return &Display{ch: make(chan progress.Msg, 64)}
}

// Start spawns the consume goroutine. Idempotent.
func (p *Display) Start(ctx context.Context) error {
	p.once.Do(func() {
		p.ctx = ctx
		p.wg.Go(p.consume)
	})
	return nil
}

// Send delivers a progress message to the display. Safe for concurrent use.
func (p *Display) Send(msg progress.Msg) {
	p.ch <- msg
}

// Shutdown closes the message channel and waits for the consume goroutine.
func (p *Display) Shutdown() error {
	p.shutdownOnce.Do(func() { close(p.ch) })
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

// jobTracker holds per-job vertex tracking state.
type jobTracker struct {
	seen         map[digest.Digest]vertexState
	pending      map[digest.Digest]timeoutVertex
	stepTimeouts map[string]time.Duration
	cmdInfos     map[string]progress.CmdInfo
}

//revive:disable-next-line:cognitive-complexity,cyclomatic,function-length consume is a linear message dispatch loop; splitting it hurts readability.
func (p *Display) consume() {
	log := slogctx.FromContext(p.ctx)
	jobs := make(map[string]*jobTracker)

	getJob := func(name string) *jobTracker {
		jt, ok := jobs[name]
		if !ok {
			jt = &jobTracker{
				seen:    make(map[digest.Digest]vertexState),
				pending: make(map[digest.Digest]timeoutVertex),
			}
			jobs[name] = jt
		}
		return jt
	}

	for msg := range p.ch {
		switch msg := msg.(type) {
		case progress.JobAddedMsg:
			jt := getJob(msg.Job)
			jt.stepTimeouts = msg.StepTimeouts
			jt.cmdInfos = msg.CmdInfos

		case progress.JobStatusMsg:
			jt := getJob(msg.Job)
			for _, v := range msg.Status.Vertexes {
				cfgTimeout := jt.stepTimeouts[v.Name]
				logVertex(logVertexInput{
					ctx:        p.ctx,
					log:        log,
					jobName:    msg.Job,
					v:          v,
					seen:       jt.seen,
					cleanName:  v.Name,
					cfgTimeout: cfgTimeout,
					cmdInfos:   jt.cmdInfos,
				})
				trackTimeout(v, jt.seen, jt.pending, v.Name, cfgTimeout)
			}
			logLogs(p.ctx, log, msg.Job, msg.Status.Logs)

		case progress.JobDoneMsg:
			if jt, ok := jobs[msg.Job]; ok {
				logPendingTimeouts(p.ctx, log, msg.Job, jt.pending)
			}

		case progress.JobSkippedMsg:
			log.LogAttrs(p.ctx, slog.LevelInfo, "job skipped",
				slog.String("job", msg.Job),
				slog.String("event", "job.skipped"),
			)

		case progress.StepSkippedMsg:
			log.LogAttrs(p.ctx, slog.LevelInfo, "step skipped",
				slog.String("job", msg.Job),
				slog.String("step", msg.Step),
				slog.String("event", "step.skipped"),
			)

		case progress.JobRetryMsg:
			attrs := []slog.Attr{
				slog.String("job", msg.Job),
				slog.Int("attempt", msg.Attempt),
				slog.Int("max_attempts", msg.MaxAttempts),
			}
			if msg.Err != nil {
				attrs = append(attrs, slog.String("error", msg.Err.Error()))
			}
			attrs = append(attrs, slog.String("event", "job.retry"))
			log.LogAttrs(p.ctx, slog.LevelWarn, "retrying job", attrs...)

		case progress.StepRetryMsg:
			attrs := []slog.Attr{
				slog.String("job", msg.Job),
				slog.String("step", msg.Step),
				slog.Int("attempt", msg.Attempt),
				slog.Int("max_attempts", msg.MaxAttempts),
			}
			if msg.Err != nil {
				attrs = append(attrs, slog.String("error", msg.Err.Error()))
			}
			attrs = append(attrs, slog.String("event", "step.retry"))
			log.LogAttrs(p.ctx, slog.LevelWarn, "retrying step", attrs...)

		case progress.StepAllowedFailureMsg:
			attrs := []slog.Attr{
				slog.String("job", msg.Job),
				slog.String("step", msg.Step),
			}
			if msg.Err != nil {
				attrs = append(attrs, slog.String("error", msg.Err.Error()))
			}
			attrs = append(attrs, slog.String("event", "step.allowed_failure"))
			log.LogAttrs(p.ctx, slog.LevelWarn, "step failed (allowed)", attrs...)

		case progress.JobTimeoutMsg:
			log.LogAttrs(p.ctx, slog.LevelWarn, "job timed out",
				slog.String("job", msg.Job),
				slog.Duration("timeout", msg.Timeout),
				slog.String("event", "job.timeout"),
			)

		case progress.LogMsg:
			attrs := []slog.Attr{slog.String("event", "log")}
			if msg.Job != "" {
				attrs = append(attrs, slog.String("job", msg.Job))
			}
			//nolint:sloglint // dynamic msg from LogMsg payload
			log.LogAttrs(p.ctx, msg.Level, msg.Message, attrs...)

		case progress.ErrorMsg:
			humanMsg := "unknown error"
			if msg.Err != nil {
				humanMsg = msg.Err.Human
			}
			//nolint:sloglint // dynamic msg from DisplayError payload
			log.LogAttrs(p.ctx, slog.LevelError, humanMsg,
				slog.String("event", "error"),
			)

		default:
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
	cmdInfos   map[string]progress.CmdInfo
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
			exitInfo := progress.ExitInfo(in.v.Error)
			attrs := append(base, slog.String("event", "vertex.error"), slog.String("error", exitInfo))
			if ci, ok := in.cmdInfos[in.cleanName]; ok && ci.FullCmd != "" {
				attrs = append(attrs, slog.String("full_cmd", ci.FullCmd))
			}
			//nolint:sloglint // dynamic msg encodes user-facing formatted output
			in.log.LogAttrs(in.ctx, slog.LevelError, fmt.Sprintf("[%s] FAIL %s: %s", in.jobName, in.cleanName, exitInfo), attrs...)
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
// reached a terminal state before the channel closed.
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
// based on their current state in seen.
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
