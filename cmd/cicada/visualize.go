package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/ndisidore/cicada/internal/visualize"
	"github.com/ndisidore/cicada/pkg/pipeline"
)

func (a *app) visualizeAction(_ context.Context, cmd *cli.Command) error {
	path := cmd.Args().First()
	if path == "" {
		return errors.New("usage: cicada visualize <file | ->")
	}

	p, err := a.parse(path)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	if _, err := pipeline.Validate(&p); err != nil {
		return fmt.Errorf("validating %s: %w", path, err)
	}

	g := visualize.BuildGraph(p)

	outPath := cmd.String("output")
	if outPath == "" {
		return visualize.RenderTerminal(g, a.stdout)
	}
	if ext := strings.ToLower(filepath.Ext(outPath)); !visualize.IsSupportedOutputExt(ext) {
		return fmt.Errorf("unsupported output format %q (use .d2 or .dot)", filepath.Ext(outPath))
	}

	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".cicada-visualize-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	emitErr := visualize.EmitToFile(g, outPath, tmp)
	closeErr := tmp.Close()
	if emitErr != nil {
		_ = os.Remove(tmpPath) //nolint:gosec // tmpPath comes from os.CreateTemp, not user input
		return fmt.Errorf("writing %s: %w", outPath, emitErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath) //nolint:gosec // tmpPath comes from os.CreateTemp, not user input
		return fmt.Errorf("closing temp file: %w", closeErr)
	}
	if err := os.Rename(tmpPath, outPath); err != nil { //nolint:gosec // tmpPath comes from os.CreateTemp, not user input
		_ = os.Remove(tmpPath) //nolint:gosec // tmpPath comes from os.CreateTemp, not user input
		return fmt.Errorf("renaming to %s: %w", outPath, err)
	}
	return nil
}
