// Package main provides the CLI entry point for ciro.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/tonistiigi/fsutil"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"

	"github.com/ndisidore/ciro/internal/builder"
	"github.com/ndisidore/ciro/internal/daemon"
	"github.com/ndisidore/ciro/internal/imagestore"
	"github.com/ndisidore/ciro/internal/progress"
	"github.com/ndisidore/ciro/internal/runner"
	"github.com/ndisidore/ciro/internal/synccontext"
	"github.com/ndisidore/ciro/pkg/parser"
	"github.com/ndisidore/ciro/pkg/pipeline"
)

// errOfflineMissingImages indicates that offline mode was requested but some
// pipeline images are not present in the BuildKit cache.
var errOfflineMissingImages = errors.New("offline mode: images not cached")

func main() {
	cmd := &cli.Command{
		Name:  "ciro",
		Usage: "a container-native CI/CD pipeline runner",
		Commands: []*cli.Command{
			{
				Name:      "validate",
				Usage:     "validate a KDL pipeline file",
				ArgsUsage: "<file>",
				Action:    validateAction,
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
						Usage: "fail if images are not cached (use 'ciro pull' first)",
					},
					&cli.BoolFlag{
						Name:  "boring",
						Usage: "use ASCII instead of emoji in TUI output",
					},
				),
				Action: runAction,
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
				Action: pullAction,
			},
			{
				Name:  "engine",
				Usage: "manage the local BuildKit engine",
				Commands: []*cli.Command{
					{
						Name:   "start",
						Usage:  "start the BuildKit engine",
						Action: engineStartAction,
					},
					{
						Name:   "stop",
						Usage:  "stop the BuildKit engine",
						Action: engineStopAction,
					},
					{
						Name:   "status",
						Usage:  "show engine status",
						Action: engineStatusAction,
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

func validateAction(_ context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: ciro validate <file>")
	}

	p, err := parser.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if _, err := p.Validate(); err != nil {
		return fmt.Errorf("validating %s: %w", path, err)
	}

	printPipelineSummary(p.Name, p.Steps)
	return nil
}

func runAction(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: ciro run <file>")
	}

	p, err := parser.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	cwd, err := os.Getwd()
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

	if cmd.Bool("dry-run") {
		printDryRun(p.Name, result)
		return nil
	}

	addr, err := resolveAddr(ctx, cmd)
	if err != nil {
		return err
	}

	c, err := connectBuildKit(ctx, addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if cmd.Bool("offline") {
		if err := checkOffline(ctx, c, p, path); err != nil {
			return err
		}
	}

	contextFS, err := fsutil.NewFS(cwd)
	if err != nil {
		return fmt.Errorf("opening context directory %s: %w", cwd, err)
	}

	display, err := selectDisplay(cmd.String("progress"), cmd.Bool("boring"))
	if err != nil {
		return err
	}

	return runner.Run(ctx, runner.RunInput{
		Client: c,
		Result: result,
		LocalMounts: map[string]fsutil.FS{
			"context": contextFS,
		},
		Display: display,
	})
}

func printDryRun(name string, result builder.Result) {
	_, _ = fmt.Printf("Pipeline '%s' validated\n", name)
	_, _ = fmt.Printf("  Steps: %d\n", len(result.Definitions))
	for i, stepName := range result.StepNames {
		ops := 0
		if result.Definitions[i] != nil {
			ops = len(result.Definitions[i].Def)
		}
		_, _ = fmt.Printf("    - %s (%d LLB ops)\n", stepName, ops)
	}
}

// resolveAddr returns the BuildKit daemon address, starting the daemon if needed.
func resolveAddr(ctx context.Context, cmd *cli.Command) (string, error) {
	addr := cmd.String("addr")
	if cmd.Bool("no-daemon") {
		return addr, nil
	}
	mgr := daemon.NewManager()
	var err error
	addr, err = mgr.EnsureRunning(ctx, addr)
	if err != nil {
		return "", fmt.Errorf("ensuring buildkitd: %w", err)
	}
	return addr, nil
}

func pullAction(ctx context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: ciro pull <file>")
	}

	p, err := parser.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	images := imagestore.CollectImages(p)
	if len(images) == 0 {
		_, _ = fmt.Println("No images to pull")
		return nil
	}

	addr, err := resolveAddr(ctx, cmd)
	if err != nil {
		return err
	}

	c, err := connectBuildKit(ctx, addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	display, err := selectDisplay(cmd.String("progress"), cmd.Bool("boring"))
	if err != nil {
		return err
	}

	if err := imagestore.PullImages(ctx, c, images, display); err != nil {
		return fmt.Errorf("pulling images: %w", err)
	}

	_, _ = fmt.Printf("Pulled %d image(s)\n", len(images))
	return nil
}

func checkOffline(ctx context.Context, c *bkclient.Client, p pipeline.Pipeline, path string) error {
	images := imagestore.CollectImages(p)
	missing, err := imagestore.CheckCached(ctx, c, images)
	if err != nil {
		return fmt.Errorf("checking image cache: %w", err)
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf(
			"%d image(s) not cached: %s\nrun 'ciro pull %s' first",
			len(missing), strings.Join(missing, ", "), path,
		)
		return fmt.Errorf("%w: %s", errOfflineMissingImages, msg)
	}
	return nil
}

// connectBuildKit creates a BuildKit client.
// bkclient.New is lazy (no network I/O); timeouts are enforced
// per-operation at Solve/ListWorkers call sites downstream.
func connectBuildKit(ctx context.Context, addr string) (*bkclient.Client, error) {
	c, err := bkclient.New(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("connecting to buildkitd at %s: %w", addr, err)
	}
	return c, nil
}

func engineStartAction(ctx context.Context, _ *cli.Command) error {
	mgr := daemon.NewManager()
	addr, err := mgr.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting engine: %w", err)
	}
	_, _ = fmt.Printf("BuildKit engine started at %s\n", addr)
	return nil
}

func engineStopAction(ctx context.Context, _ *cli.Command) error {
	mgr := daemon.NewManager()
	stopErr := mgr.Stop(ctx)
	removeErr := mgr.Remove(ctx)
	if err := errors.Join(stopErr, removeErr); err != nil {
		return fmt.Errorf("stopping engine: %w", err)
	}
	_, _ = fmt.Println("BuildKit engine stopped")
	return nil
}

func engineStatusAction(ctx context.Context, _ *cli.Command) error {
	mgr := daemon.NewManager()
	state, err := mgr.Status(ctx)
	if err != nil {
		return fmt.Errorf("checking engine status: %w", err)
	}
	if state == "" {
		_, _ = fmt.Println("BuildKit engine: not running")
	} else {
		_, _ = fmt.Printf("BuildKit engine: %s\n", state)
	}
	return nil
}

func selectDisplay(mode string, boring bool) (progress.Display, error) {
	switch mode {
	case "auto":
		if term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("CI") == "" {
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

func printPipelineSummary(name string, steps []pipeline.Step) {
	_, _ = fmt.Printf("Pipeline '%s' is valid\n", name)
	_, _ = fmt.Printf("  Steps: %d\n", len(steps))
	for _, s := range steps {
		_, _ = fmt.Printf("    - %s (image: %s)\n", s.Name, s.Image)
	}
}
