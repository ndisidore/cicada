// Package synccontext loads ignore patterns for build context filtering.
package synccontext

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoIgnoreFile indicates neither .ciroignore nor .dockerignore was found.
var ErrNoIgnoreFile = errors.New("no ignore file found")

const (
	_ciroIgnore   = ".ciroignore"
	_dockerIgnore = ".dockerignore"
)

// LoadIgnorePatterns reads .ciroignore (or .dockerignore fallback) from dir.
// Returns ErrNoIgnoreFile when neither file exists.
func LoadIgnorePatterns(dir string) ([]string, error) {
	path := filepath.Join(dir, _ciroIgnore)
	patterns, err := readPatternFile(path)
	if err == nil {
		return patterns, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", _ciroIgnore, err)
	}

	path = filepath.Join(dir, _dockerIgnore)
	patterns, err = readPatternFile(path)
	if err == nil {
		return patterns, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoIgnoreFile
	}
	return nil, fmt.Errorf("reading %s: %w", _dockerIgnore, err)
}

// readPatternFile parses a newline-delimited ignore file.
// Blank lines and comments (lines starting with #) are skipped.
func readPatternFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	patterns := []string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning ignore file: %w", err)
	}
	return patterns, nil
}
