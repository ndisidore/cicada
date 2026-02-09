// Package main provides the CLI entry point for ciro.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/tonistiigi/fsutil"
	"github.com/urfave/cli/v3"

	"github.com/ndisidore/ciro/internal/builder"
	"github.com/ndisidore/ciro/internal/runner"
	"github.com/ndisidore/ciro/pkg/parser"
	"github.com/ndisidore/ciro/pkg/pipeline"
)

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
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "addr",
						Usage: "BuildKit daemon address (unix socket or tcp://host:port)",
						Value: "unix:///run/buildkit/buildkitd.sock",
					},
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "generate LLB without executing",
					},
					&cli.BoolFlag{
						Name:  "no-cache",
						Usage: "disable BuildKit cache for all steps",
					},
				},
				Action: runAction,
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

	result, err := builder.Build(ctx, p, builder.BuildOpts{
		NoCache: cmd.Bool("no-cache"),
	})
	if err != nil {
		return fmt.Errorf("building %s: %w", path, err)
	}

	if cmd.Bool("dry-run") {
		_, _ = fmt.Printf("Pipeline '%s' validated\n", p.Name)
		_, _ = fmt.Printf("  Steps: %d\n", len(result.Definitions))
		for i, name := range result.StepNames {
			ops := 0
			if i < len(result.Definitions) && result.Definitions[i] != nil {
				ops = len(result.Definitions[i].Def)
			}
			_, _ = fmt.Printf("    - %s (%d LLB ops)\n", name, ops)
		}
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	contextFS, err := fsutil.NewFS(cwd)
	if err != nil {
		return fmt.Errorf("opening context directory %s: %w", cwd, err)
	}

	return runner.Run(ctx, runner.RunInput{
		Addr:   cmd.String("addr"),
		Result: result,
		LocalMounts: map[string]fsutil.FS{
			"context": contextFS,
		},
	})
}

func printPipelineSummary(name string, steps []pipeline.Step) {
	_, _ = fmt.Printf("Pipeline '%s' is valid\n", name)
	_, _ = fmt.Printf("  Steps: %d\n", len(steps))
	for _, s := range steps {
		_, _ = fmt.Printf("    - %s (image: %s)\n", s.Name, s.Image)
	}
}
