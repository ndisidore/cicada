package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
)

// extractOutputs solves an output extraction definition to a temp directory,
// reads the output file, parses KEY=VALUE lines, and returns the result.
func extractOutputs(ctx context.Context, def *llb.Definition, cfg runConfig) (map[string]string, error) {
	dir, err := os.MkdirTemp("", "cicada-output-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ch := make(chan *client.SolveStatus)
	go drainChannel(ch)

	_, err = cfg.solver.Solve(ctx, def, client.SolveOpt{
		Exports: []client.ExportEntry{{
			Type:      client.ExporterLocal,
			OutputDir: dir,
		}},
		CacheImports: cfg.cacheImports,
		Session:      cfg.session,
	}, ch)
	if err != nil {
		return nil, fmt.Errorf("solving output extraction: %w", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "output"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading output file: %w", err)
	}

	return parseOutputLines(string(data)), nil
}

// parseOutputLines parses KEY=VALUE lines from a $CICADA_OUTPUT file.
// Empty lines and lines without '=' are skipped.
func parseOutputLines(content string) map[string]string {
	result := make(map[string]string)
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			continue
		}
		result[key] = value
	}
	return result
}
