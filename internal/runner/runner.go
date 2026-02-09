// Package runner executes BuildKit LLB definitions against a buildkitd daemon.
package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/opencontainers/go-digest"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"

	"github.com/ndisidore/ciro/internal/builder"
)

const _connectTimeout = 30 * time.Second

// RunInput holds parameters for executing a pipeline against BuildKit.
type RunInput struct {
	Addr        string
	Result      builder.Result
	LocalMounts map[string]fsutil.FS
}

// Run executes each step's LLB definition sequentially against a BuildKit daemon.
func Run(ctx context.Context, in RunInput) error {
	if len(in.Result.Definitions) != len(in.Result.StepNames) {
		return fmt.Errorf(
			"result mismatch: %d definitions vs %d step names",
			len(in.Result.Definitions), len(in.Result.StepNames),
		)
	}

	log := slog.Default()
	log.DebugContext(ctx, "connecting to buildkitd", slog.String("addr", in.Addr))

	connCtx, cancel := context.WithTimeout(ctx, _connectTimeout)
	defer cancel()

	c, err := client.New(connCtx, in.Addr)
	if err != nil {
		return fmt.Errorf("connecting to buildkitd at %s: %w", in.Addr, err)
	}
	defer c.Close()

	for i, def := range in.Result.Definitions {
		name := in.Result.StepNames[i]
		log.Info("starting step", slog.String("step", name))

		if err := solveStep(ctx, c, log, name, def, in.LocalMounts); err != nil {
			return fmt.Errorf("step %q: %w", name, err)
		}

		log.Info("completed step", slog.String("step", name))
	}

	return nil
}

func solveStep(
	ctx context.Context,
	c *client.Client,
	log *slog.Logger,
	name string,
	def *llb.Definition,
	localMounts map[string]fsutil.FS,
) error {
	ch := make(chan *client.SolveStatus)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		_, err := c.Solve(ctx, def, client.SolveOpt{
			LocalMounts: localMounts,
		}, ch)
		return err
	})

	g.Go(func() error {
		return displayProgress(ctx, log, name, ch)
	})

	return g.Wait()
}

// vertexState tracks what we've already printed for each vertex.
type vertexState int

const (
	_stateUnseen vertexState = iota
	_stateStarted
	_stateDone
)

func displayProgress(ctx context.Context, log *slog.Logger, stepName string, ch chan *client.SolveStatus) error {
	seen := make(map[digest.Digest]vertexState)

	for status := range ch {
		for _, v := range status.Vertexes {
			printVertex(ctx, log, stepName, v, seen)
		}
		for _, l := range status.Logs {
			if len(l.Data) > 0 {
				msg := strings.TrimRight(string(l.Data), "\n")
				if msg != "" {
					log.LogAttrs(ctx, slog.LevelInfo, "output",
						slog.String("step", stepName),
						slog.String("data", msg),
					)
				}
			}
		}
	}
	return nil
}

func printVertex(ctx context.Context, log *slog.Logger, stepName string, v *client.Vertex, seen map[digest.Digest]vertexState) {
	prev := seen[v.Digest]

	attrs := []slog.Attr{
		slog.String("step", stepName),
		slog.String("vertex", v.Name),
	}

	switch {
	case v.Error != "":
		log.LogAttrs(ctx, slog.LevelError, "vertex error",
			append(attrs, slog.String("error", v.Error))...,
		)
		seen[v.Digest] = _stateDone
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
}
