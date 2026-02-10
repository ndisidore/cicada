// Package parser converts KDL documents into pipeline definitions.
package parser

import (
	"errors"
	"fmt"
	"io"
	"os"
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
)

// ParseFile reads and parses a KDL pipeline file at the given path.
func ParseFile(path string) (p pipeline.Pipeline, err error) {
	f, err := os.Open(path)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing %s: %w", path, cerr)
		}
	}()

	return Parse(f, path)
}

// Parse parses KDL content from the reader into a Pipeline.
func Parse(r io.Reader, filename string) (pipeline.Pipeline, error) {
	doc, err := kdl.Parse(r)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("parsing %s: %w", filename, err)
	}

	var pipelineNode *document.Node
	for _, node := range doc.Nodes {
		if node.Name.ValueString() == "pipeline" {
			if pipelineNode != nil {
				return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, ErrMultiplePipelines)
			}
			pipelineNode = node
		}
	}

	if pipelineNode == nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, ErrNoPipeline)
	}

	return parsePipeline(pipelineNode, filename)
}

// ParseString parses KDL content from a string into a Pipeline.
func ParseString(content string) (pipeline.Pipeline, error) {
	return Parse(strings.NewReader(content), "<string>")
}

func parsePipeline(node *document.Node, filename string) (pipeline.Pipeline, error) {
	name, err := stringArg(node, 0)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf(
			"%s: %w: %w", filename, ErrMissingName, err,
		)
	}

	p := pipeline.Pipeline{
		Name:  name,
		Steps: make([]pipeline.Step, 0, len(node.Children)),
	}

	for _, child := range node.Children {
		switch childName := child.Name.ValueString(); childName {
		case "step":
			step, err := parseStep(child, filename)
			if err != nil {
				return pipeline.Pipeline{}, err
			}
			p.Steps = append(p.Steps, step)
		case "matrix":
			if p.Matrix != nil {
				return pipeline.Pipeline{}, fmt.Errorf(
					"%s: %w: %q", filename, ErrDuplicateField, "matrix",
				)
			}
			m, err := parseMatrix(child, filename, "pipeline")
			if err != nil {
				return pipeline.Pipeline{}, err
			}
			p.Matrix = &m
		default:
			return pipeline.Pipeline{}, fmt.Errorf(
				"%s: %w: %q (expected step or matrix)", filename, ErrUnknownNode, childName,
			)
		}
	}

	p, err = pipeline.Expand(p)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}

	if _, err := p.Validate(); err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}

	return p, nil
}

func parseStep(node *document.Node, filename string) (pipeline.Step, error) {
	name, err := stringArg(node, 0)
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
	childName := node.Name.ValueString()

	// Single-arg fields share the same extraction pattern.
	switch childName {
	case "image", "run", "workdir", "depends-on":
		v, err := requireStringArg(node, filename, childName)
		if err != nil {
			return err
		}
		return applySingleArgField(s, childName, v, filename)
	case "mount":
		m, err := stringArgs2(node, filename, "mount")
		if err != nil {
			return err
		}
		ro, err := boolProp(node, "readonly")
		if err != nil {
			return fmt.Errorf("%s: step %q: mount: %w", filename, s.Name, err)
		}
		s.Mounts = append(s.Mounts, pipeline.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
		return nil
	case "cache":
		c, err := stringArgs2(node, filename, "cache")
		if err != nil {
			return err
		}
		s.Caches = append(s.Caches, pipeline.Cache{ID: c[0], Target: c[1]})
		return nil
	case "matrix":
		if s.Matrix != nil {
			return fmt.Errorf(
				"%s: step %q: %w: %q", filename, s.Name, ErrDuplicateField, "matrix",
			)
		}
		m, err := parseMatrix(node, filename, fmt.Sprintf("step %q", s.Name))
		if err != nil {
			return err
		}
		s.Matrix = &m
		return nil
	default:
		return fmt.Errorf(
			"%s: step %q: %w: %q", filename, s.Name, ErrUnknownNode, childName,
		)
	}
}

func applySingleArgField(s *pipeline.Step, field, value, filename string) error {
	switch field {
	case "image":
		if s.Image != "" {
			return fmt.Errorf(
				"%s: step %q: %w: %q", filename, s.Name, ErrDuplicateField, field,
			)
		}
		s.Image = value
	case "run":
		s.Run = append(s.Run, value)
	case "workdir":
		if s.Workdir != "" {
			return fmt.Errorf(
				"%s: step %q: %w: %q", filename, s.Name, ErrDuplicateField, field,
			)
		}
		s.Workdir = value
	case "depends-on":
		s.DependsOn = append(s.DependsOn, value)
	default:
		panic(fmt.Sprintf("applySingleArgField: unexpected field %q for step %q in %s", field, s.Name, filename))
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
			v, ok := child.Arguments[i].ResolvedValue().(string)
			if !ok {
				return pipeline.Matrix{}, fmt.Errorf(
					"%s: %s: matrix dimension %q value %d: not a string: %w",
					filename, scope, dimName, i, ErrTypeMismatch,
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
	first, err := stringArg(node, 0)
	if err != nil {
		return [2]string{}, fmt.Errorf(
			"%s: %s first argument: %w", filename, field, err,
		)
	}
	second, err := stringArg(node, 1)
	if err != nil {
		return [2]string{}, fmt.Errorf(
			"%s: %s second argument: %w", filename, field, err,
		)
	}
	return [2]string{first, second}, nil
}

// requireStringArg extracts the first string argument, wrapping errors with context.
func requireStringArg(node *document.Node, filename, field string) (string, error) {
	v, err := stringArg(node, 0)
	if err != nil {
		return "", fmt.Errorf(
			"%s: %q requires a string value: %w", filename, field, err,
		)
	}
	return v, nil
}

// stringArg returns the string value at the given argument index, or an error.
func stringArg(node *document.Node, idx int) (string, error) {
	if idx >= len(node.Arguments) {
		return "", fmt.Errorf("argument %d: %w", idx, ErrMissingField)
	}
	v, ok := node.Arguments[idx].ResolvedValue().(string)
	if !ok {
		return "", fmt.Errorf("argument %d: not a string: %w", idx, ErrTypeMismatch)
	}
	return v, nil
}

// boolProp reads an optional boolean property from a node.
// Returns false when the property is absent.
func boolProp(node *document.Node, key string) (bool, error) {
	v, ok := node.Properties[key]
	if !ok {
		return false, nil
	}
	b, ok := v.ResolvedValue().(bool)
	if !ok {
		return false, fmt.Errorf("property %q: not a boolean: %w", key, ErrTypeMismatch)
	}
	return b, nil
}
