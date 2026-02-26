// Package main provides the CLI entry point for cicada.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/ndisidore/cicada/internal/daemon"
	"github.com/ndisidore/cicada/internal/progress"
	"github.com/ndisidore/cicada/internal/progress/plain"
	"github.com/ndisidore/cicada/internal/progress/progressmodel"
	"github.com/ndisidore/cicada/internal/progress/quiet"
	"github.com/ndisidore/cicada/internal/progress/tui"
	"github.com/ndisidore/cicada/internal/runner/runnermodel"
	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/internal/runtime/docker"
	"github.com/ndisidore/cicada/internal/runtime/podman"
	"github.com/ndisidore/cicada/pkg/parser"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// errResultMismatch indicates builder.Result has mismatched Definitions and JobNames lengths.
var errResultMismatch = errors.New("builder.Result: definitions/job-names length mismatch")

// errUnknownBuilderJob indicates a builder job name not found in the pipeline.
var errUnknownBuilderJob = errors.New("builder job not found in pipeline")

// errOfflineMissingImages indicates that offline mode was requested but some
// pipeline images are not present in the BuildKit cache.
var errOfflineMissingImages = errors.New("offline mode: images not cached")

// Engine abstracts daemon lifecycle operations for testability.
type Engine interface {
	// EnsureRunning ensures the daemon is running at the given address, starting it if needed.
	EnsureRunning(ctx context.Context, addr string) (string, error)
	// Start starts the BuildKit daemon and returns its address.
	Start(ctx context.Context) (string, error)
	// Stop stops the BuildKit daemon.
	Stop(ctx context.Context) error
	// Remove removes the BuildKit daemon container.
	Remove(ctx context.Context) error
	// Status returns the current daemon state, or "" if not running.
	Status(ctx context.Context) (string, error)
}

// app bundles dependencies so CLI action handlers become testable methods.
type app struct {
	engine  Engine
	rt      runtime.Runtime
	connect func(ctx context.Context, addr string) (runnermodel.Solver, func() error, error)
	parse   func(path string) (pipelinemodel.Pipeline, error)
	getwd   func() (string, error)
	stdout  io.Writer
	isTTY   bool
	format  string // resolved output format (pretty, json, text)
}

func main() {
	p := &parser.Parser{Resolver: &parser.StdinResolver{
		Stdin: os.Stdin,
		Inner: &parser.FileResolver{},
	}}
	a := &app{
		connect: defaultConnect,
		parse:   p.ParseFile,
		getwd:   os.Getwd,
		stdout:  os.Stdout,
		isTTY:   term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("CI") == "", //nolint:gosec // G115: file descriptors are small non-negative integers, safe to cast uintptr->int
	}

	named := map[runtime.Type]func() runtime.Runtime{
		runtime.Docker: func() runtime.Runtime { return docker.New() },
		runtime.Podman: func() runtime.Runtime { return podman.New() },
	}
	resolver := &runtime.Resolver{
		Factories: []func() runtime.Runtime{named[runtime.Docker], named[runtime.Podman]},
		Named:     named,
	}
	initEngine := func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		rtName := cmd.String("runtime")
		rt, err := resolver.Get(ctx, rtName)
		if err != nil {
			return ctx, fmt.Errorf("resolve runtime %s: %w", rtName, err)
		}
		mgr, err := daemon.NewManager(rt)
		if err != nil {
			return ctx, fmt.Errorf("create daemon manager for runtime %s: %w", rtName, err)
		}
		a.engine = mgr
		a.rt = rt
		return ctx, nil
	}

	cmd := &cli.Command{
		Name:  "cicada",
		Usage: "a container-native CI/CD pipeline runner",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "runtime",
				Usage:   "container runtime (auto, docker, podman)",
				Value:   "auto",
				Sources: cli.EnvVars("CICADA_RUNTIME"),
			},
			&cli.StringFlag{
				Name:    "format",
				Usage:   "output format (auto, pretty, json, text)",
				Value:   "auto",
				Sources: cli.EnvVars("CICADA_FORMAT"),
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "log level (debug, info, warn, error)",
				Value:   "info",
				Sources: cli.EnvVars("CICADA_LOG_LEVEL"),
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			a.format = cmd.String("format")
			if a.format == "auto" {
				if a.isTTY {
					a.format = "pretty"
				} else {
					a.format = "text"
				}
			}
			var level slog.Level
			if err := level.UnmarshalText([]byte(cmd.String("log-level"))); err != nil {
				return ctx, fmt.Errorf("invalid log level %q: %w", cmd.String("log-level"), err)
			}
			logger, err := progress.NewLogger(a.stdout, a.format, level)
			if err != nil {
				return ctx, fmt.Errorf("initializing logger: %w", err)
			}
			slog.SetDefault(logger)
			return slogctx.ContextWithLogger(ctx, logger), nil
		},
		Commands: []*cli.Command{
			{
				Name:      "validate",
				Usage:     "validate a KDL pipeline file",
				ArgsUsage: "<file | ->",
				Action:    a.validateAction,
			},
			{
				Name:      "visualize",
				Usage:     "render a pipeline as a flow diagram",
				ArgsUsage: "<file | ->",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "output",
						Aliases: []string{"o"},
						Usage:   "write diagram to file (.d2 or .dot); default: terminal (Unicode box-drawing)",
					},
				},
				Action: a.visualizeAction,
			},
			{
				Name:               "run",
				Usage:              "run a KDL pipeline against BuildKit",
				ArgsUsage:          "<file | ->",
				Before:             initEngine,
				SliceFlagSeparator: ";",
				Flags: append(buildkitFlags(),
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "generate LLB without executing",
					},
					&cli.BoolFlag{
						Name:  "no-cache",
						Usage: "disable BuildKit cache for all jobs",
					},
					&cli.StringSliceFlag{
						Name:  "no-cache-filter",
						Usage: "disable cache for specific jobs by name",
					},
					&cli.BoolFlag{
						Name:  "offline",
						Usage: "fail if images are not cached (use 'cicada pull' first)",
					},
					&cli.BoolFlag{
						Name:  "boring",
						Usage: "use ASCII instead of emoji in TUI output",
					},
					&cli.IntFlag{
						Name:    "parallelism",
						Aliases: []string{"j"},
						Usage:   "max concurrent jobs (0 = unlimited)",
						Value:   0,
					},
					&cli.BoolFlag{
						Name:  "fail-fast",
						Usage: "cancel all jobs on first failure (disable with --fail-fast=false to let independent jobs finish)",
						Value: true,
					},
					&cli.BoolFlag{
						Name:  "expose-deps",
						Usage: "mount full dependency root filesystems at /deps/{name}",
					},
					&cli.StringFlag{
						Name:  "start-at",
						Usage: "run from this job (or job:step) forward",
					},
					&cli.StringFlag{
						Name:  "stop-after",
						Usage: "run up to and including this job (or job:step)",
					},
					&cli.StringSliceFlag{
						Name:  "cache-to",
						Usage: "cache export destination (e.g. type=registry,ref=ghcr.io/user/cache)",
					},
					&cli.StringSliceFlag{
						Name:  "cache-from",
						Usage: "cache import source (e.g. type=registry,ref=ghcr.io/user/cache)",
					},
					&cli.BoolFlag{
						Name:  "cache-stats",
						Usage: "print cache hit/miss statistics after run",
					},
					&cli.StringSliceFlag{
						Name:  "with-docker-export",
						Usage: "load published images into local Docker daemon (job names, or '*' for all)",
					},
					&cli.BoolFlag{
						Name:  "no-sync-cache",
						Usage: "disable content-hash file sync cache",
					},
				),
				Action: a.runAction,
			},
			{
				Name:      "pull",
				Usage:     "pre-pull pipeline images into the BuildKit cache",
				ArgsUsage: "<file | ->",
				Before:    initEngine,
				Flags: append(buildkitFlags(),
					&cli.BoolFlag{
						Name:  "boring",
						Usage: "use ASCII instead of emoji in TUI output",
					},
				),
				Action: a.pullAction,
			},
			{
				Name:   "engine",
				Usage:  "manage the local BuildKit engine",
				Before: initEngine,
				Commands: []*cli.Command{
					{
						Name:   "start",
						Usage:  "start the BuildKit engine",
						Action: a.engineStartAction,
					},
					{
						Name:   "stop",
						Usage:  "stop the BuildKit engine",
						Action: a.engineStopAction,
					},
					{
						Name:   "status",
						Usage:  "show engine status",
						Action: a.engineStatusAction,
					},
				},
			},
		},
		ExitErrHandler: func(_ context.Context, _ *cli.Command, err error) {
			if err != nil {
				var de *progressmodel.DisplayError
				if errors.As(err, &de) {
					_, _ = fmt.Fprintf(os.Stderr, "error: %s\n", de.Human)
				} else {
					_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
				}
			}
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		os.Exit(1)
	}
}

// defaultConnect creates a BuildKit client and returns it as a runnermodel.Solver.
// bkclient.New is lazy (no network I/O), so we eagerly verify reachability
// via ListWorkers. This also warms the gRPC/HTTP2 connection so that
// subsequent Solve calls can establish session streams immediately,
// preventing BuildKit's 5-second session-lookup timeout from firing
// when secrets require the session during early cache-key computation.
func defaultConnect(ctx context.Context, addr string) (runnermodel.Solver, func() error, error) {
	c, err := bkclient.New(ctx, addr)
	if err != nil {
		return nil, func() error { return nil }, fmt.Errorf("connecting to buildkitd at %s: %w", addr, err)
	}
	if _, err := c.ListWorkers(ctx); err != nil {
		_ = c.Close()
		return nil, func() error { return nil }, fmt.Errorf("reaching buildkitd at %s: %w", addr, err)
	}
	return c, c.Close, nil
}

// buildkitFlags returns the shared flag set for commands that connect to BuildKit.
func buildkitFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:  "addr",
			Usage: "BuildKit daemon address (unix socket or tcp://host:port)",
			Value: daemon.DefaultAddr(),
		},
		&cli.BoolFlag{
			Name:  "no-daemon",
			Usage: "disable automatic buildkitd management",
		},
		&cli.StringFlag{
			Name:  "progress",
			Usage: "progress output mode (auto, tui, plain, quiet)",
			Value: "auto",
		},
	}
}

func (a *app) selectDisplay(mode string, boring bool) (progressmodel.Display, error) {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "auto":
		if a.isTTY && a.format == "pretty" {
			return tui.New(boring), nil
		}
		return plain.New(), nil
	case "tui":
		return tui.New(boring), nil
	case "plain":
		return plain.New(), nil
	case "quiet":
		return quiet.New(), nil
	default:
		return nil, fmt.Errorf("unknown progress mode %q (valid: auto, tui, plain, quiet)", mode)
	}
}
