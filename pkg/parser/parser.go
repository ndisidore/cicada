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
	ErrUnknownNode    = errors.New("unknown node type")
	ErrMissingField   = errors.New("missing required field")
	ErrDuplicateField = errors.New("duplicate field")
	ErrExtraArgs      = errors.New("too many arguments")
	ErrTypeMismatch   = errors.New("argument type mismatch")
	ErrUnknownProp    = errors.New("unknown property")
	ErrAmbiguousFile  = errors.New("ambiguous file: contains both pipeline and fragment nodes")
	ErrEmptyInclude   = errors.New("no pipeline or fragment node found")
	ErrNilResolver    = errors.New("Parser.Resolver is nil")
)

// Parser converts KDL documents into validated pipeline definitions.
type Parser struct {
	// Resolver opens include sources during parsing. Must be non-nil before
	// calling ParseFile or ParseString.
	Resolver Resolver
}

// ParseFile reads and parses a KDL pipeline file at the given path.
func (p *Parser) ParseFile(path string) (pipeline.Pipeline, error) {
	if p.Resolver == nil {
		return pipeline.Pipeline{}, ErrNilResolver
	}
	rc, resolved, err := p.Resolver.Resolve(path, "")
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = rc.Close() }()

	return p.parse(rc, resolved)
}

// _syntheticFilename identifies in-memory content for dirOf fallback.
const _syntheticFilename = "<string>"

// ParseString parses KDL content from a string into a Pipeline.
// Includes are resolved relative to the current working directory.
func (p *Parser) ParseString(content string) (pipeline.Pipeline, error) {
	return p.parse(strings.NewReader(content), _syntheticFilename)
}

// parse is the core parse entry: dispatches root nodes directly.
func (p *Parser) parse(r io.Reader, filename string) (pipeline.Pipeline, error) {
	if p.Resolver == nil {
		return pipeline.Pipeline{}, ErrNilResolver
	}
	doc, err := parseKDL(r, filename)
	if err != nil {
		return pipeline.Pipeline{}, err
	}

	gc := newGroupCollector(filename)
	var acc pipelineAcc
	state := newIncludeState()

	for _, node := range doc.Nodes {
		if err := p.parsePipelineChild(node, filename, state, gc, &acc); err != nil {
			return pipeline.Pipeline{}, err
		}
	}

	merged, err := gc.merge()
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}

	return finalizePipeline(finalizePipelineInput{
		Name:     nameFromFilename(filename),
		Jobs:     merged,
		Env:      acc.Env,
		Matrix:   acc.Matrix,
		Defaults: acc.Defaults,
		Aliases:  state.aliases,
		Filename: filename,
	})
}

// pipelineAcc accumulates pipeline-level fields parsed from child nodes.
type pipelineAcc struct {
	Matrix   *pipeline.Matrix
	Defaults *pipeline.Defaults
	Env      []pipeline.EnvVar
}

// parsePipelineChild dispatches a single child node of a pipeline block.
//
//revive:disable-next-line:cognitive-complexity parsePipelineChild is a flat switch dispatch; splitting it hurts readability.
func (p *Parser) parsePipelineChild(
	child *document.Node,
	filename string,
	state *includeState,
	gc *groupCollector,
	acc *pipelineAcc,
) error {
	switch nt := NodeType(child.Name.ValueString()); nt {
	case NodeTypeStep:
		// Bare step sugar: desugar into a single-step job.
		job, err := parseBareStep(child, filename)
		if err != nil {
			return err
		}
		gc.addJob(job)
	case NodeTypeJob:
		job, err := parseJob(child, filename)
		if err != nil {
			return err
		}
		gc.addJob(job)
	case NodeTypeDefaults:
		if acc.Defaults != nil {
			return fmt.Errorf("%s: pipeline: %w: %q", filename, ErrDuplicateField, string(nt))
		}
		d, err := parseDefaults(child, filename)
		if err != nil {
			return err
		}
		acc.Defaults = &d
	case NodeTypeMatrix:
		m, err := parseMatrix(child, filename, "pipeline")
		if err != nil {
			return err
		}
		if err := setOnce(&acc.Matrix, &m, filename, "pipeline", string(nt)); err != nil {
			return err
		}
	case NodeTypeEnv:
		ev, err := parseEnvNode(child, filename, "pipeline")
		if err != nil {
			return err
		}
		acc.Env = append(acc.Env, ev)
	case NodeTypeInclude:
		jobs, inc, err := p.resolveChildInclude(child, filename, state)
		if err != nil {
			return err
		}
		gc.addInclude(jobs, inc)
	default:
		return fmt.Errorf(
			"%s: %w: %q (expected job, step, defaults, matrix, env, or include)", filename, ErrUnknownNode, string(nt),
		)
	}
	return nil
}

// finalizePipelineInput groups parameters for finalizePipeline.
type finalizePipelineInput struct {
	Name     string
	Jobs     []pipeline.Job
	Env      []pipeline.EnvVar
	Matrix   *pipeline.Matrix
	Defaults *pipeline.Defaults
	Aliases  map[string][]string
	Filename string
}

// finalizePipeline applies defaults, expands aliases and matrices, then validates.
func finalizePipeline(in finalizePipelineInput) (pipeline.Pipeline, error) {
	jobs, err := pipeline.ExpandAliases(in.Jobs, in.Aliases)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", in.Filename, err)
	}

	// Apply defaults before expansion and validation.
	jobs = pipeline.ApplyDefaults(jobs, in.Defaults)

	pl := pipeline.Pipeline{Name: in.Name, Jobs: jobs, Env: in.Env, Matrix: in.Matrix, Defaults: in.Defaults}
	pl, err = pipeline.Expand(pl)
	if err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", in.Filename, err)
	}

	if _, err := pl.Validate(); err != nil {
		return pipeline.Pipeline{}, fmt.Errorf("%s: %w", in.Filename, err)
	}
	return pl, nil
}

// --- Job parsing ---

// parseJob parses a job KDL node into a pipeline.Job.
func parseJob(node *document.Node, filename string) (pipeline.Job, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeJob))
	if err != nil {
		return pipeline.Job{}, fmt.Errorf(
			"%s: job missing name: %w: %w", filename, ErrMissingField, err,
		)
	}

	j := pipeline.Job{Name: name}
	for _, child := range node.Children {
		if err := applyJobField(&j, child, filename); err != nil {
			return pipeline.Job{}, err
		}
	}
	return j, nil
}

// applyJobField dispatches a single child node of a job block.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic,function-length applyJobField is a flat switch dispatch; splitting it hurts readability.
func applyJobField(j *pipeline.Job, node *document.Node, filename string) error {
	nt := NodeType(node.Name.ValueString())
	scope := fmt.Sprintf("job %q", j.Name)
	switch nt {
	case NodeTypeImage:
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		return setOnce(&j.Image, v, filename, scope, string(nt))
	case NodeTypeWorkdir:
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		return setOnce(&j.Workdir, v, filename, scope, string(nt))
	case NodeTypePlatform:
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		return setOnce(&j.Platform, v, filename, scope, string(nt))
	case NodeTypeDependsOn:
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		j.DependsOn = append(j.DependsOn, v)
	case NodeTypeRun:
		return fmt.Errorf(
			"%s: %s: %w: %q (run must be inside a step)", filename, scope, ErrUnknownNode, string(nt),
		)
	case NodeTypeMount:
		m, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		ro, err := prop[bool](node, PropReadonly)
		if err != nil {
			return fmt.Errorf("%s: %s: mount: %w", filename, scope, err)
		}
		j.Mounts = append(j.Mounts, pipeline.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
	case NodeTypeCache:
		c, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		j.Caches = append(j.Caches, pipeline.Cache{ID: c[0], Target: c[1]})
	case NodeTypeEnv:
		ev, err := parseEnvNode(node, filename, scope)
		if err != nil {
			return err
		}
		j.Env = append(j.Env, ev)
	case NodeTypeExport:
		exp, err := parseExportNode(node, filename, j.Name)
		if err != nil {
			return err
		}
		j.Exports = append(j.Exports, exp)
	case NodeTypeArtifact:
		art, err := parseArtifactNode(node, filename, j.Name)
		if err != nil {
			return err
		}
		j.Artifacts = append(j.Artifacts, art)
	case NodeTypeMatrix:
		m, err := parseMatrix(node, filename, scope)
		if err != nil {
			return err
		}
		if err := setOnce(&j.Matrix, &m, filename, scope, string(nt)); err != nil {
			return err
		}
	case NodeTypeNoCache:
		if len(node.Arguments) > 0 {
			return fmt.Errorf("%s: %s: no-cache takes no arguments: %w", filename, scope, ErrExtraArgs)
		}
		if j.NoCache {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		j.NoCache = true
	case NodeTypeStep:
		step, err := parseJobStep(node, filename, j.Name)
		if err != nil {
			return err
		}
		j.Steps = append(j.Steps, step)
	default:
		return fmt.Errorf(
			"%s: %s: %w: %q", filename, scope, ErrUnknownNode, string(nt),
		)
	}
	return nil
}

// --- Step parsing (within a job) ---

// parseJobStep parses a step KDL node within a job into a pipeline.Step.
func parseJobStep(node *document.Node, filename, jobName string) (pipeline.Step, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeStep))
	if err != nil {
		return pipeline.Step{}, fmt.Errorf(
			"%s: job %q: step missing name: %w: %w", filename, jobName, ErrMissingField, err,
		)
	}

	s := pipeline.Step{Name: name}
	for _, child := range node.Children {
		if err := applyJobStepField(&s, child, filename, jobName); err != nil {
			return pipeline.Step{}, err
		}
	}
	return s, nil
}

// applyJobStepField dispatches a single child node of a step block within a job.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic applyJobStepField is a flat switch dispatch over node types; splitting it hurts readability.
func applyJobStepField(s *pipeline.Step, node *document.Node, filename, jobName string) error {
	nt := NodeType(node.Name.ValueString())
	scope := fmt.Sprintf("job %q step %q", jobName, s.Name)
	switch nt {
	case NodeTypeRun:
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		s.Run = append(s.Run, v)
	case NodeTypeEnv:
		ev, err := parseEnvNode(node, filename, scope)
		if err != nil {
			return err
		}
		s.Env = append(s.Env, ev)
	case NodeTypeWorkdir:
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		return setOnce(&s.Workdir, v, filename, scope, string(nt))
	case NodeTypeMount:
		m, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		ro, err := prop[bool](node, PropReadonly)
		if err != nil {
			return fmt.Errorf("%s: %s: mount: %w", filename, scope, err)
		}
		s.Mounts = append(s.Mounts, pipeline.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
	case NodeTypeCache:
		c, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		s.Caches = append(s.Caches, pipeline.Cache{ID: c[0], Target: c[1]})
	case NodeTypeExport:
		exp, err := parseExportNode(node, filename, scope)
		if err != nil {
			return err
		}
		s.Exports = append(s.Exports, exp)
	case NodeTypeArtifact:
		art, err := parseArtifactNode(node, filename, scope)
		if err != nil {
			return err
		}
		s.Artifacts = append(s.Artifacts, art)
	case NodeTypeNoCache:
		if len(node.Arguments) > 0 {
			return fmt.Errorf("%s: %s: no-cache takes no arguments: %w", filename, scope, ErrExtraArgs)
		}
		if s.NoCache {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		s.NoCache = true
	default:
		return fmt.Errorf(
			"%s: %s: %w: %q", filename, scope, ErrUnknownNode, string(nt),
		)
	}
	return nil
}

// --- Bare step desugaring ---

// parseBareStep parses a step node directly under a pipeline into a single-step
// Job. Job-level fields (image, platform, depends-on, etc.) go to the Job;
// run commands go to the inner Step.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic parseBareStep is a flat switch dispatch over node types; splitting it hurts readability.
func parseBareStep(node *document.Node, filename string) (pipeline.Job, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeStep))
	if err != nil {
		return pipeline.Job{}, fmt.Errorf(
			"%s: step missing name: %w: %w", filename, ErrMissingField, err,
		)
	}

	j := pipeline.Job{Name: name}
	s := pipeline.Step{Name: name}

	for _, child := range node.Children {
		nt := NodeType(child.Name.ValueString())
		switch nt {
		case NodeTypeRun:
			// run goes to the inner step.
			v, err := requireStringArg(child, filename, string(nt))
			if err != nil {
				return pipeline.Job{}, err
			}
			s.Run = append(s.Run, v)
		case NodeTypeStep:
			return pipeline.Job{}, fmt.Errorf(
				"%s: step %q: %w: nested %q (use job for multi-step)",
				filename, name, ErrUnknownNode, string(nt),
			)
		default:
			// All other fields go to the job.
			if err := applyJobField(&j, child, filename); err != nil {
				return pipeline.Job{}, err
			}
		}
	}

	j.Steps = []pipeline.Step{s}
	return j, nil
}

// --- Defaults parsing ---

// parseDefaults parses a defaults KDL node into a pipeline.Defaults.
//
//revive:disable-next-line:cognitive-complexity parseDefaults is a flat switch dispatch over child node types; splitting it hurts readability.
func parseDefaults(node *document.Node, filename string) (pipeline.Defaults, error) {
	if len(node.Arguments) > 0 {
		return pipeline.Defaults{}, fmt.Errorf(
			"%s: defaults takes no arguments: %w", filename, ErrExtraArgs,
		)
	}
	var d pipeline.Defaults
	for _, child := range node.Children {
		nt := NodeType(child.Name.ValueString())
		switch nt {
		case NodeTypeImage:
			v, err := requireStringArg(child, filename, string(nt))
			if err != nil {
				return pipeline.Defaults{}, err
			}
			if err := setOnce(&d.Image, v, filename, "defaults", string(nt)); err != nil {
				return pipeline.Defaults{}, err
			}
		case NodeTypeWorkdir:
			v, err := requireStringArg(child, filename, string(nt))
			if err != nil {
				return pipeline.Defaults{}, err
			}
			if err := setOnce(&d.Workdir, v, filename, "defaults", string(nt)); err != nil {
				return pipeline.Defaults{}, err
			}
		case NodeTypeMount:
			m, err := stringArgs2(child, filename, string(nt))
			if err != nil {
				return pipeline.Defaults{}, err
			}
			ro, err := prop[bool](child, PropReadonly)
			if err != nil {
				return pipeline.Defaults{}, fmt.Errorf("%s: defaults: mount: %w", filename, err)
			}
			d.Mounts = append(d.Mounts, pipeline.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
		case NodeTypeEnv:
			ev, err := parseEnvNode(child, filename, "defaults")
			if err != nil {
				return pipeline.Defaults{}, err
			}
			d.Env = append(d.Env, ev)
		default:
			return pipeline.Defaults{}, fmt.Errorf(
				"%s: defaults: %w: %q (expected image, workdir, mount, or env)",
				filename, ErrUnknownNode, string(nt),
			)
		}
	}
	return d, nil
}

// --- KDL helpers ---

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

// parseEnvNode extracts a key-value pair from an env node (two string args).
func parseEnvNode(node *document.Node, filename, scope string) (pipeline.EnvVar, error) {
	kv, err := stringArgs2(node, filename, string(NodeTypeEnv))
	if err != nil {
		return pipeline.EnvVar{}, fmt.Errorf("%s: %s: %w", filename, scope, err)
	}
	return pipeline.EnvVar{Key: kv[0], Value: kv[1]}, nil
}

// parseExportNode extracts an export path and required local property.
func parseExportNode(node *document.Node, filename, scopeName string) (pipeline.Export, error) {
	if len(node.Arguments) > 1 {
		return pipeline.Export{}, fmt.Errorf(
			"%s: %q: export requires exactly one argument, got %d: %w",
			filename, scopeName, len(node.Arguments), ErrExtraArgs,
		)
	}
	path, err := requireStringArg(node, filename, string(NodeTypeExport))
	if err != nil {
		return pipeline.Export{}, fmt.Errorf("%s: %q: export: %w", filename, scopeName, err)
	}
	local, err := prop[string](node, PropLocal)
	if err != nil {
		return pipeline.Export{}, fmt.Errorf("%s: %q: export: %w", filename, scopeName, err)
	}
	if local == "" {
		return pipeline.Export{}, fmt.Errorf(
			"%s: %q: export: local property: %w", filename, scopeName, ErrMissingField,
		)
	}
	return pipeline.Export{Path: path, Local: local}, nil
}

// parseArtifactNode extracts an artifact definition (three string args: from, source, target).
func parseArtifactNode(node *document.Node, filename, scopeName string) (pipeline.Artifact, error) {
	args, err := stringArgs3(node, string(NodeTypeArtifact))
	if err != nil {
		return pipeline.Artifact{}, fmt.Errorf("%s: %q: %w", filename, scopeName, err)
	}
	return pipeline.Artifact{From: args[0], Source: args[1], Target: args[2]}, nil
}

// stringArgs3 extracts exactly three string arguments from a node.
func stringArgs3(node *document.Node, field string) ([3]string, error) {
	switch {
	case len(node.Arguments) < 3:
		return [3]string{}, fmt.Errorf(
			"%s requires exactly three arguments, got %d: %w",
			field, len(node.Arguments), ErrMissingField,
		)
	case len(node.Arguments) > 3:
		return [3]string{}, fmt.Errorf(
			"%s requires exactly three arguments, got %d: %w",
			field, len(node.Arguments), ErrExtraArgs,
		)
	}
	first, err := arg[string](node, 0)
	if err != nil {
		return [3]string{}, fmt.Errorf("%s first argument: %w", field, err)
	}
	second, err := arg[string](node, 1)
	if err != nil {
		return [3]string{}, fmt.Errorf("%s second argument: %w", field, err)
	}
	third, err := arg[string](node, 2)
	if err != nil {
		return [3]string{}, fmt.Errorf("%s third argument: %w", field, err)
	}
	return [3]string{first, second, third}, nil
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
