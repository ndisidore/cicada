package progress

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/opencontainers/go-digest"
)

// vertexState tracks what has already been printed for each vertex.
type vertexState int

const (
	_stateUnseen vertexState = iota
	_stateStarted
	_stateDone
)

// Plain renders progress using structured slog output.
type Plain struct {
	Log *slog.Logger
}

// Run consumes SolveStatus events and logs them via slog.
// It returns when ch is closed or ctx is cancelled.
func (p *Plain) Run(ctx context.Context, jobName string, ch <-chan *client.SolveStatus) error {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}

	seen := make(map[digest.Digest]vertexState)

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("progress display cancelled: %w", ctx.Err())
		case status, ok := <-ch:
			if !ok {
				return nil
			}
			for _, v := range status.Vertexes {
				if err := printVertex(ctx, log, jobName, v, seen); err != nil {
					return err
				}
			}
			printLogs(ctx, log, jobName, status.Logs)
		}
	}
}

func printLogs(ctx context.Context, log *slog.Logger, jobName string, logs []*client.VertexLog) {
	for _, l := range logs {
		if len(l.Data) == 0 {
			continue
		}
		msg := strings.TrimSpace(string(l.Data))
		if msg != "" {
			log.LogAttrs(ctx, slog.LevelInfo, "output",
				slog.String("job", jobName),
				slog.String("data", msg),
			)
		}
	}
}

func printVertex(ctx context.Context, log *slog.Logger, jobName string, v *client.Vertex, seen map[digest.Digest]vertexState) error {
	prev := seen[v.Digest]

	attrs := []slog.Attr{
		slog.String("job", jobName),
		slog.String("vertex", v.Name),
	}

	switch {
	case v.Error != "":
		log.LogAttrs(ctx, slog.LevelError, "vertex error",
			append(attrs, slog.String("error", v.Error))...,
		)
		seen[v.Digest] = _stateDone
		return fmt.Errorf("vertex %q: %s", v.Name, v.Error)
	case v.Cached && prev < _stateDone:
		log.LogAttrs(ctx, slog.LevelInfo, "cached", attrs...)
		seen[v.Digest] = _stateDone
	case v.Completed != nil && prev < _stateDone:
		if v.Started != nil {
			dur := v.Completed.Sub(*v.Started).Round(time.Millisecond)
			attrs = append(attrs, slog.Duration("duration", dur))
		}
		log.LogAttrs(ctx, slog.LevelInfo, "done", attrs...)
		seen[v.Digest] = _stateDone
	case v.Started != nil && prev < _stateStarted:
		log.LogAttrs(ctx, slog.LevelInfo, "started", attrs...)
		seen[v.Digest] = _stateStarted
	default:
		// No state change; nothing to display.
	}
	return nil
}
