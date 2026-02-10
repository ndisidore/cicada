// Package parser converts KDL documents into pipeline definitions.
package parser

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	kdl "github.com/sblinch/kdl-go"
	"github.com/sblinch/kdl-go/document"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// Sentinel errors for parse failures.
var (
	ErrNoPipeline        = errors.New("no pipeline node found")
	ErrMultiplePipelines = errors.New("multiple pipeline nodes found")
	ErrMissingName       = errors.New("pipeline node missing name argument")
	ErrUnknownNode       = errors.New("unknown node type")
	ErrMissingField      = errors.New("missing required field")
	ErrDuplicateField    = errors.New("duplicate field")
	ErrExtraArgs         = errors.New("too many arguments")
	ErrTypeMismatch      = errors.New("argument type mismatch")
	ErrUnknownProp       = errors.New("unknown property")
	ErrAmbiguousFile     = errors.New("contains both pipeline and fragment nodes")
)

// Parser converts KDL documents into validated pipeline definitions.
type Parser struct {
	// Resolver opens include sources during parsing. Must be non-nil before
	// calling ParseFile.
	Resolver Resolver
}

// ParseFile reads and parses a KDL pipeline file at the given path.
func (p *Parser) ParseFile(path string) (pipeline.Pipeline, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("resolving %s: %w", path, err)
	}

	rc, resolved, err := p.Resolver.Resolve(absPath, "")
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = rc.Close() }()

	return p.parse(rc, resolved)
}

// ParseString parses KDL content from a string into a Pipeline.
// Includes are resolved relative to the current working directory.
func (p *Parser) ParseString(content string) (pipeline.Pipeline, error) {
	return p.parse(strings.NewReader(content), "<string>")
}

// parse is the core parse entry: detects top-level pipeline node and delegates.
func (p *Parser) parse(r io.Reader, filename string) (pipeline.Pipeline, error) {
	doc, err := parseKDL(r, filename)
	if err != nil {
		return pipeline.Pipeline{}, err
	}

	var pipelineNode *document.Node
	for _, node := range doc.Nodes {
		if NodeType(node.Name.ValueString()) == NodeTypePipeline {
			if pipelineNode != nil {
				return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, ErrMultiplePipelines)
			}
			pipelineNode = node
		}
	}

	if pipelineNode == nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, ErrNoPipeline)
	}

	return p.parsePipeline(pipelineNode, filename, newIncludeState())
}

// parsePipeline parses a pipeline node with include-aware child resolution.
func (p *Parser) parsePipeline(node *document.Node, filename string, state *includeState) (pipeline.Pipeline, error) {
	name, err := requireStringArg(node, filename, string(NodeTypePipeline))
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf(
			"%s: %w: %w", filename, ErrMissingName, err,
		)
	}

	gc := newGroupCollector(filename)
	var mat *pipeline.Matrix

	for _, child := range node.Children {
		switch nt := NodeType(child.Name.ValueString()); nt {
		case NodeTypeStep:
			step, err := parseStep(child, filename)
			if err != nil {
				return pipeline.Pipeline{}, err
			}
			gc.addStep(step)

		case NodeTypeMatrix:
			m, err := parseMatrix(child, filename, "pipeline")
			if err != nil {
				return pipeline.Pipeline{}, err
			}
			if err := setOnce(&mat, &m, filename, "pipeline", string(NodeTypeMatrix)); err != nil {
				return pipeline.Pipeline{}, err
			}

		case NodeTypeInclude:
			steps, inc, err := p.resolveChildInclude(child, filename, state)
			if err != nil {
				return pipeline.Pipeline{}, err
			}
			gc.addInclude(steps, inc)

		default:
			return pipeline.Pipeline{}, fmt.Errorf(
				"%s: %w: %q (expected step, matrix, or include)", filename, ErrUnknownNode, string(nt),
			)
		}
	}

	merged, err := gc.merge()
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}

	return finalizePipeline(name, merged, mat, state.aliases, filename)
}

// finalizePipeline expands aliases, applies matrix expansion, and validates.
func finalizePipeline(name string, steps []pipeline.Step, mat *pipeline.Matrix, aliases map[string][]string, filename string) (pipeline.Pipeline, error) {
	steps, err := pipeline.ExpandAliases(steps, aliases)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}

	pl := pipeline.Pipeline{Name: name, Steps: steps, Matrix: mat}
	pl, err = pipeline.Expand(pl)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}

	if _, err := pl.Validate(); err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}
	return pl, nil
}

// --- KDL helpers (unchanged logic, extracted for reuse) ---

// parseKDL wraps kdl.Parse with a filename context on errors.
func parseKDL(r io.Reader, filename string) (*document.Document, error) {
	doc, err := kdl.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", filename, err)
	}
	return doc, nil
}

// dirOf returns the directory portion of a file path, suitable for resolving
// relative includes. For synthetic filenames (like "<string>"), returns ".".
func dirOf(filename string) string {
	if strings.HasPrefix(filename, "<") {
		return "."
	}
	return filepath.Dir(filename)
}

func parseStep(node *document.Node, filename string) (pipeline.Step, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeStep))
	if err != nil {
		return pipeline.Step{}, fmt.Errorf(
			"%s: step missing name: %w: %w", filename, ErrMissingField, err,
		)
	}

	s := pipeline.Step{Name: name}
	for _, child := range node.Children {
		if err := applyStepField(&s, child, filename); err != nil {
			return pipeline.Step{}, err
		}
	}
	return s, nil
}

func applyStepField(s *pipeline.Step, node *document.Node, filename string) error {
	nt := NodeType(node.Name.ValueString())
	switch nt {
	case NodeTypeImage, NodeTypeWorkdir, NodeTypeRun, NodeTypeDependsOn, NodeTypePlatform:
		return applyStringField(s, nt, node, filename)
	case NodeTypeMount:
		m, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		ro, err := prop[bool](node, PropReadonly)
		if err != nil {
			return fmt.Errorf("%s: step %q: mount: %w", filename, s.Name, err)
		}
		s.Mounts = append(s.Mounts, pipeline.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
	case NodeTypeCache:
		c, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		s.Caches = append(s.Caches, pipeline.Cache{ID: c[0], Target: c[1]})
	case NodeTypeMatrix:
		m, err := parseMatrix(node, filename, fmt.Sprintf("step %q", s.Name))
		if err != nil {
			return err
		}
		if err := setOnce(&s.Matrix, &m, filename, fmt.Sprintf("step %q", s.Name), string(nt)); err != nil {
			return err
		}
	default:
		return fmt.Errorf(
			"%s: step %q: %w: %q", filename, s.Name, ErrUnknownNode, string(nt),
		)
	}
	return nil
}

func applyStringField(s *pipeline.Step, nt NodeType, node *document.Node, filename string) error {
	v, err := requireStringArg(node, filename, string(nt))
	if err != nil {
		return err
	}
	scope := fmt.Sprintf("step %q", s.Name)
	switch nt {
	case NodeTypeImage:
		return setOnce(&s.Image, v, filename, scope, string(nt))
	case NodeTypeWorkdir:
		return setOnce(&s.Workdir, v, filename, scope, string(nt))
	case NodeTypePlatform:
		return setOnce(&s.Platform, v, filename, scope, string(nt))
	case NodeTypeRun:
		s.Run = append(s.Run, v)
	case NodeTypeDependsOn:
		s.DependsOn = append(s.DependsOn, v)
	default:
		return fmt.Errorf(
			"%s: step %q: %w: %q", filename, s.Name, ErrUnknownNode, string(nt),
		)
	}
	return nil
}

// parseMatrix parses a matrix block into a pipeline.Matrix. Each child node
// is a dimension where the node name is the dimension name and arguments are
// the string values. Structural validation (empty matrix, duplicate/invalid
// dimension names, empty dimensions, combination cap) is delegated to
// pipeline.ValidateMatrix.
func parseMatrix(node *document.Node, filename, scope string) (pipeline.Matrix, error) {
	m := pipeline.Matrix{
		Dimensions: make([]pipeline.Dimension, 0, len(node.Children)),
	}

	for _, child := range node.Children {
		dimName := child.Name.ValueString()
		values := make([]string, 0, len(child.Arguments))
		for i := range child.Arguments {
			v, err := arg[string](child, i)
			if err != nil {
				return pipeline.Matrix{}, fmt.Errorf(
					"%s: %s: matrix dimension %q value %d: %w",
					filename, scope, dimName, i, err,
				)
			}
			values = append(values, v)
		}
		m.Dimensions = append(m.Dimensions, pipeline.Dimension{Name: dimName, Values: values})
	}

	if err := pipeline.ValidateMatrix(&m); err != nil {
		return pipeline.Matrix{}, fmt.Errorf("%s: %s: matrix: %w", filename, scope, err)
	}

	return m, nil
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
