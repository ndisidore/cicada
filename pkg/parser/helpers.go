//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	kdl "github.com/calico32/kdl-go"
)

// parseKDL wraps kdl.Parse with a filename context on errors.
func parseKDL(r io.Reader, filename string) (*kdl.Document, error) {
	doc, err := kdl.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	return doc, nil
}

// childrenOf returns the child nodes of a KDL node.
func childrenOf(node *kdl.Node) []*kdl.Node {
	return node.Children().Nodes
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

// coerceValue extracts a typed Go value from a kdl.Value, returning ErrTypeMismatch
// when the value's kind does not match T.
func coerceValue[T any](v kdl.Value, ctx string) (T, error) {
	var zero T
	switch any(zero).(type) {
	case string:
		if v.Kind() != kdl.String {
			return zero, fmt.Errorf("%s: not a %T: %w", ctx, zero, ErrTypeMismatch)
		}
		//revive:disable-next-line:unchecked-type-assertion T is constrained to string by the type-switch case guard.
		return any(v.String()).(T), nil
	case int:
		if v.Kind() != kdl.Int {
			return zero, fmt.Errorf("%s: not a %T: %w", ctx, zero, ErrTypeMismatch)
		}
		//revive:disable-next-line:unchecked-type-assertion T is constrained to int by the type-switch case guard.
		return any(v.Int()).(T), nil
	case bool:
		if v.Kind() != kdl.Bool {
			return zero, fmt.Errorf("%s: not a %T: %w", ctx, zero, ErrTypeMismatch)
		}
		//revive:disable-next-line:unchecked-type-assertion T is constrained to bool by the type-switch case guard.
		return any(v.Bool()).(T), nil
	default:
		return zero, fmt.Errorf("%s: unsupported type %T: %w", ctx, zero, ErrTypeMismatch)
	}
}

// stringArgs2 extracts exactly two string arguments from a node.
func stringArgs2(node *kdl.Node, filename, field string) ([2]string, error) {
	args := node.Arguments()
	switch {
	case len(args) < 2:
		return [2]string{}, fmt.Errorf(
			"%s: %s requires exactly two arguments, got %d: %w",
			filename, field, len(args), ErrMissingField,
		)
	case len(args) > 2:
		return [2]string{}, fmt.Errorf(
			"%s: %s requires exactly two arguments, got %d: %w",
			filename, field, len(args), ErrExtraArgs,
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
func requireStringArg(node *kdl.Node, filename, field string) (string, error) {
	v, err := arg[string](node, 0)
	if err != nil {
		return "", fmt.Errorf(
			"%s: %q requires a string value: %w", filename, field, err,
		)
	}
	return v, nil
}

// arg returns the typed value at the given argument index, or an error.
func arg[T any](node *kdl.Node, idx int) (T, error) {
	var zero T
	args := node.Arguments()
	if idx >= len(args) {
		return zero, fmt.Errorf("argument %d: %w", idx, ErrMissingField)
	}
	return coerceValue[T](args[idx], fmt.Sprintf("argument %d", idx))
}

// prop reads an optional typed property from a node.
// Returns the zero value when the property is absent.
func prop[T any](node *kdl.Node, key string) (T, error) {
	var zero T
	v, ok := node.Properties()[key]
	if !ok {
		return zero, nil
	}
	return coerceValue[T](v, fmt.Sprintf("property %q", key))
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
