//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Sentinel errors for StdinResolver misconfiguration.
var (
	ErrNilStdin = errors.New("stdin resolver: Stdin is nil")
	ErrNilInner = errors.New("stdin resolver: Inner resolver is nil")
)

// Resolver opens include sources for reading. Implementations return the
// content reader, the resolved absolute path (used for cycle detection and
// relative include resolution), and any error.
type Resolver interface {
	Resolve(source string, basePath string) (io.ReadCloser, string, error)
}

// StdinResolver intercepts the "-" source to read from standard input,
// delegating all other sources to the wrapped Inner resolver.
type StdinResolver struct {
	Stdin io.Reader
	Inner Resolver
}

// Resolve returns stdin when source is "-", otherwise delegates to Inner.
func (s *StdinResolver) Resolve(source string, basePath string) (io.ReadCloser, string, error) {
	if source == "-" {
		if s.Stdin == nil {
			return nil, "", fmt.Errorf("resolve source %q: %w", source, ErrNilStdin)
		}
		return io.NopCloser(s.Stdin), _stdinFilename, nil
	}
	if s.Inner == nil {
		return nil, "", fmt.Errorf("resolve source %q: %w", source, ErrNilInner)
	}
	return s.Inner.Resolve(source, basePath)
}

// FileResolver resolves includes from the local filesystem.
type FileResolver struct{}

// Resolve opens a local file relative to basePath and returns its reader and
// absolute path.
func (*FileResolver) Resolve(source string, basePath string) (io.ReadCloser, string, error) {
	path := source
	if !filepath.IsAbs(source) {
		path = filepath.Join(basePath, source)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", fmt.Errorf("resolving include %q from %s: %w", source, basePath, err)
	}

	f, err := os.Open(abs)
	if err != nil {
		return nil, "", fmt.Errorf("resolving include %q from %s: %w", source, basePath, err)
	}
	return f, abs, nil
}
