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
	"github.com/tonistiigi/fsutil"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/ndisidore/cicada/internal/builder"
	"github.com/ndisidore/cicada/internal/daemon"
	"github.com/ndisidore/cicada/internal/imagestore"
	"github.com/ndisidore/cicada/internal/progress"
	"github.com/ndisidore/cicada/internal/runner"
	"github.com/ndisidore/cicada/internal/synccontext"
	"github.com/ndisidore/cicada/pkg/parser"
	"github.com/ndisidore/cicada/pkg/pipeline"
)

// errResultMismatch indicates builder.Result has mismatched Definitions and StepNames lengths.
var errResultMismatch = errors.New("builder.Result: definitions/step-names length mismatch")

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
	connect func(ctx context.Context, addr string) (runner.Solver, func() error, error)
	parse   func(path string) (pipeline.Pipeline, error)
	getwd   func() (string, error)
	stdout  io.Writer
	isTTY   bool
}

func main() {
	a := &app{
		engine:  daemon.NewManager(),
		connect: defaultConnect,
		parse:   parser.ParseFile,
		getwd:   os.Getwd,
		stdout:  os.Stdout,
		isTTY:   term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("CI") == "",
	}

	cmd := &cli.Command{
		Name:  "cicada",
		Usage: "a container-native CI/CD pipeline runner",
		Commands: []*cli.Command{
			{
				Name:      "validate",
				Usage:     "validate a KDL pipeline file",
				ArgsUsage: "<file>",
				Action:    a.validateAction,
			},
			{
				Name:      "run",
				Usage:     "run a KDL pipeline against BuildKit",
				ArgsUsage: "<file>",
				Flags: append(buildkitFlags(),
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "generate LLB without executing",
					},
					&cli.BoolFlag{
						Name:  "no-cache",
						Usage: "disable BuildKit cache for all steps",
					},
					&cli.BoolFlag{
						Name:  "offline",
						Usage: "fail if images are not cached (use 'cicada pull' first)",
					},
					&cli.BoolFlag{
						Name:  "boring",
						Usage: "use ASCII instead of emoji in TUI output",
					},
				),
				Action: a.runAction,
			},
			{
				Name:      "pull",
				Usage:     "pre-pull pipeline images into the BuildKit cache",
				ArgsUsage: "<file>",
				Flags: append(buildkitFlags(),
					&cli.BoolFlag{
						Name:  "boring",
						Usage: "use ASCII instead of emoji in TUI output",
					},
				),
				Action: a.pullAction,
			},
			{
				Name:  "engine",
				Usage: "manage the local BuildKit engine",
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
				_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
		},
	}

	if err := cmd.Run(context.Background(), os.Args); err != nil {
		os.Exit(1)
	}
}

// defaultConnect creates a BuildKit client and returns it as a runner.Solver.
// bkclient.New is lazy (no network I/O); timeouts are enforced
// per-operation at Solve/ListWorkers call sites downstream.
func defaultConnect(ctx context.Context, addr string) (runner.Solver, func() error, error) {
	c, err := bkclient.New(ctx, addr)
	if err != nil {
		return nil, func() error { return nil }, fmt.Errorf("connecting to buildkitd at %s: %w", addr, err)
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
			Usage: "progress output mode (auto, plain, quiet)",
			Value: "auto",
		},
	}
}

func (a *app) validateAction(_ context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada validate <file>")
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if _, err := p.Validate(); err != nil {
		return fmt.Errorf("validating %s: %w", path, err)
	}

	a.printPipelineSummary(p.Name, p.Steps)
	return nil
}

func (a *app) runAction(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada run <file>")
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	cwd, err := a.getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	excludes, err := synccontext.LoadIgnorePatterns(cwd)
	if err != nil && !errors.Is(err, synccontext.ErrNoIgnoreFile) {
		return fmt.Errorf("loading ignore patterns: %w", err)
	}

	result, err := builder.Build(ctx, p, builder.BuildOpts{
		NoCache:         cmd.Bool("no-cache"),
		ExcludePatterns: excludes,
	})
	if err != nil {
		return fmt.Errorf("building %s: %w", path, err)
	}

	steps, err := buildSteps(result)
	if err != nil {
		return fmt.Errorf("converting build result: %w", err)
	}

	if cmd.Bool("dry-run") {
		a.printDryRun(p.Name, steps)
		return nil
	}

	addr, err := a.resolveAddr(ctx, cmd)
	if err != nil {
		return err
	}

	solver, closer, err := a.connect(ctx, addr)
	if err != nil {
		return err
	}
	defer func() {
		if err := closer(); err != nil {
			slog.Default().DebugContext(ctx, "close connection failed", slog.String("error", err.Error()))
		}
	}()

	if cmd.Bool("offline") {
		if err := checkOffline(ctx, solver, p, path); err != nil {
			return err
		}
	}

	contextFS, err := fsutil.NewFS(cwd)
	if err != nil {
		return fmt.Errorf("opening context directory %s: %w", cwd, err)
	}

	display, err := a.selectDisplay(cmd.String("progress"), cmd.Bool("boring"))
	if err != nil {
		return err
	}

	return runner.Run(ctx, runner.RunInput{
		Solver: solver,
		Steps:  steps,
		LocalMounts: map[string]fsutil.FS{
			"context": contextFS,
		},
		Display: display,
	})
}

func (a *app) printDryRun(name string, steps []runner.Step) {
	_, _ = fmt.Fprintf(a.stdout, "Pipeline '%s' validated\n", name)
	_, _ = fmt.Fprintf(a.stdout, "  Steps: %d\n", len(steps))
	for _, step := range steps {
		ops := 0
		if step.Definition != nil {
			ops = len(step.Definition.Def)
		}
		_, _ = fmt.Fprintf(a.stdout, "    - %s (%d LLB ops)\n", step.Name, ops)
	}
}

// buildSteps converts a builder.Result into a slice of runner.Step.
func buildSteps(r builder.Result) ([]runner.Step, error) {
	if len(r.Definitions) != len(r.StepNames) {
		return nil, fmt.Errorf("%w: %d definitions, %d step names",
			errResultMismatch, len(r.Definitions), len(r.StepNames))
	}
	steps := make([]runner.Step, len(r.Definitions))
	for i := range r.Definitions {
		steps[i] = runner.Step{
			Name:       r.StepNames[i],
			Definition: r.Definitions[i],
		}
	}
	return steps, nil
}

// resolveAddr returns the BuildKit daemon address, starting the daemon if needed.
func (a *app) resolveAddr(ctx context.Context, cmd *cli.Command) (string, error) {
	addr := cmd.String("addr")
	if cmd.Bool("no-daemon") {
		return addr, nil
	}
	var err error
	addr, err = a.engine.EnsureRunning(ctx, addr)
	if err != nil {
		return "", fmt.Errorf("ensuring buildkitd: %w", err)
	}
	return addr, nil
}

func (a *app) pullAction(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada pull <file>")
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	images := pipeline.CollectImages(p)
	if len(images) == 0 {
		_, _ = fmt.Fprintln(a.stdout, "No images to pull")
		return nil
	}

	addr, err := a.resolveAddr(ctx, cmd)
	if err != nil {
		return err
	}

	solver, closer, err := a.connect(ctx, addr)
	if err != nil {
		return err
	}
	defer func() {
		if err := closer(); err != nil {
			slog.Default().DebugContext(ctx, "close connection failed", slog.String("error", err.Error()))
		}
	}()

	display, err := a.selectDisplay(cmd.String("progress"), cmd.Bool("boring"))
	if err != nil {
		return err
	}

	if err := imagestore.PullImages(ctx, solver, images, display); err != nil {
		return fmt.Errorf("pulling images: %w", err)
	}

	_, _ = fmt.Fprintf(a.stdout, "Pulled %d image(s)\n", len(images))
	return nil
}

func checkOffline(ctx context.Context, solver runner.Solver, p pipeline.Pipeline, path string) error {
	images := pipeline.CollectImages(p)
	missing, err := imagestore.CheckCached(ctx, solver, images)
	if err != nil {
		return fmt.Errorf("checking image cache: %w", err)
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf(
			"%d image(s) not cached: %s\nrun 'cicada pull %s' first",
			len(missing), strings.Join(missing, ", "), path,
		)
		return fmt.Errorf("%w: %s", errOfflineMissingImages, msg)
	}
	return nil
}

func (a *app) engineStartAction(ctx context.Context, _ *cli.Command) error {
	addr, err := a.engine.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting engine: %w", err)
	}
	_, _ = fmt.Fprintf(a.stdout, "BuildKit engine started at %s\n", addr)
	return nil
}

func (a *app) engineStopAction(ctx context.Context, _ *cli.Command) error {
	stopErr := a.engine.Stop(ctx)
	removeErr := a.engine.Remove(ctx)
	if err := errors.Join(stopErr, removeErr); err != nil {
		return fmt.Errorf("stopping engine: %w", err)
	}
	_, _ = fmt.Fprintln(a.stdout, "BuildKit engine stopped")
	return nil
}

func (a *app) engineStatusAction(ctx context.Context, _ *cli.Command) error {
	state, err := a.engine.Status(ctx)
	if err != nil {
		return fmt.Errorf("checking engine status: %w", err)
	}
	if state == "" {
		_, _ = fmt.Fprintln(a.stdout, "BuildKit engine: not running")
	} else {
		_, _ = fmt.Fprintf(a.stdout, "BuildKit engine: %s\n", state)
	}
	return nil
}

func (a *app) printPipelineSummary(name string, steps []pipeline.Step) {
	_, _ = fmt.Fprintf(a.stdout, "Pipeline '%s' is valid\n", name)
	_, _ = fmt.Fprintf(a.stdout, "  Steps: %d\n", len(steps))
	for _, s := range steps {
		_, _ = fmt.Fprintf(a.stdout, "    - %s (image: %s)\n", s.Name, s.Image)
	}
}

func (a *app) selectDisplay(mode string, boring bool) (progress.Display, error) {
	switch mode {
	case "auto":
		if a.isTTY {
			return &progress.TUI{Boring: boring}, nil
		}
		return &progress.Plain{Log: slog.Default()}, nil
	case "plain":
		return &progress.Plain{Log: slog.Default()}, nil
	case "quiet":
		return &progress.Quiet{}, nil
	default:
		return nil, fmt.Errorf("unknown progress mode %q (valid: auto, plain, quiet)", mode)
	}
}
