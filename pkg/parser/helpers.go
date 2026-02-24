//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	kdl "github.com/sblinch/kdl-go"
	"github.com/sblinch/kdl-go/document"
)

// parseKDL wraps kdl.Parse with a filename context on errors.
func parseKDL(r io.Reader, filename string) (*document.Document, error) {
	doc, err := kdl.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	return doc, nil
}

// dirOf returns the directory portion of a file path, suitable for resolving
// relative includes. For synthetic filenames, returns ".".
func dirOf(filename string) string {
	if filename == _syntheticFilename {
		return "."
	}
	return filepath.Dir(filename)
}

// nameFromFilename derives a pipeline name from its filename (e.g. "ci.kdl" -> "ci").
func nameFromFilename(filename string) string {
	if filename == _syntheticFilename {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(filename), ".kdl")
}

// stringArgs2 extracts exactly two string arguments from a node.
func stringArgs2(node *document.Node, filename, field string) ([2]string, error) {
	switch {
	case len(node.Arguments) < 2:
		return [2]string{}, fmt.Errorf(
			"%s: %s requires exactly two arguments, got %d: %w",
			filename, field, len(node.Arguments), ErrMissingField,
		)
	case len(node.Arguments) > 2:
		return [2]string{}, fmt.Errorf(
			"%s: %s requires exactly two arguments, got %d: %w",
			filename, field, len(node.Arguments), ErrExtraArgs,
		)
	}
	first, err := arg[string](node, 0)
	if err != nil {
		return [2]string{}, fmt.Errorf(
			"%s: %s first argument: %w", filename, field, err,
		)
	}
	second, err := arg[string](node, 1)
	if err != nil {
		return [2]string{}, fmt.Errorf(
			"%s: %s second argument: %w", filename, field, err,
		)
	}
	return [2]string{first, second}, nil
}

// requireStringArg extracts the first string argument, wrapping errors with context.
func requireStringArg(node *document.Node, filename, field string) (string, error) {
	v, err := arg[string](node, 0)
	if err != nil {
		return "", fmt.Errorf(
			"%s: %q requires a string value: %w", filename, field, err,
		)
	}
	return v, nil
}

// arg returns the typed value at the given argument index, or an error.
func arg[T any](node *document.Node, idx int) (T, error) {
	var zero T
	if idx >= len(node.Arguments) {
		return zero, fmt.Errorf("argument %d: %w", idx, ErrMissingField)
	}
	v, ok := node.Arguments[idx].ResolvedValue().(T)
	if !ok {
		return zero, fmt.Errorf("argument %d: not a %T: %w", idx, zero, ErrTypeMismatch)
	}
	return v, nil
}

// prop reads an optional typed property from a node.
// Returns the zero value when the property is absent.
func prop[T any](node *document.Node, key string) (T, error) {
	var zero T
	v, ok := node.Properties[key]
	if !ok {
		return zero, nil
	}
	t, ok := v.ResolvedValue().(T)
	if !ok {
		return zero, fmt.Errorf("property %q: not a %T: %w", key, zero, ErrTypeMismatch)
	}
	return t, nil
}

// setOnce assigns value to *dst if *dst is the zero value, or returns
// ErrDuplicateField if already set.
func setOnce[T comparable](dst *T, value T, filename, scope, field string) error {
	var zero T
	if *dst != zero {
		return fmt.Errorf(
			"%s: %s: %w: %q", filename, scope, ErrDuplicateField, field,
		)
	}
	*dst = value
	return nil
}
