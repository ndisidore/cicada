// Package main provides the CLI entry point for ciro.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/urfave/cli/v2"

	"github.com/ndisidore/ciro/internal/builder"
	"github.com/ndisidore/ciro/pkg/parser"
	"github.com/ndisidore/ciro/pkg/pipeline"
)

func main() {
	app := &cli.App{
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
						Usage: "BuildKit daemon address",
						Value: "tcp://127.0.0.1:1234",
					},
					&cli.BoolFlag{
						Name:  "dry-run",
						Usage: "generate LLB without executing",
					},
				},
				Action: runAction,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		slog.Error("fatal", slog.Any("error", err))
		os.Exit(1)
	}
}

func validateAction(c *cli.Context) error {
	path := c.Args().First()
	if path == "" {
		return errors.New("usage: ciro validate <file>")
	}

	p, err := parser.ParseFile(path)
	if err != nil {
		return err
	}

	printPipelineSummary(p.Name, len(p.Steps), p.Steps)
	return nil
}

func runAction(c *cli.Context) error {
	path := c.Args().First()
	if path == "" {
		return errors.New("usage: ciro run <file>")
	}

	p, err := parser.ParseFile(path)
	if err != nil {
		return err
	}

	result, err := builder.Build(c.Context, p)
	if err != nil {
		return err
	}

	if c.Bool("dry-run") {
		_, _ = fmt.Printf("Pipeline '%s' validated\n", p.Name)
		_, _ = fmt.Printf("  Steps: %d\n", len(result.Definitions))
		for i, name := range result.StepNames {
			_, _ = fmt.Printf("    - %s (%d LLB ops)\n", name, len(result.Definitions[i].Def))
		}
		return nil
	}

	// Live execution will be implemented in Phase 4.
	_ = c.String("addr")
	return errors.New("live execution not yet implemented (use --dry-run)")
}

func printPipelineSummary(name string, count int, steps []pipeline.Step) {
	_, _ = fmt.Printf("Pipeline '%s' is valid\n", name)
	_, _ = fmt.Printf("  Steps: %d\n", count)
	for _, s := range steps {
		_, _ = fmt.Printf("    - %s (image: %s)\n", s.Name, s.Image)
	}
}
