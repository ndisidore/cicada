// Package parser converts KDL documents into pipeline definitions.
//
//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"errors"
	"fmt"
	"io"
	"strings"

	kdl "github.com/calico32/kdl-go"

	"github.com/ndisidore/cicada/pkg/pipeline"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// Sentinel errors for parse failures.
var (
	ErrUnknownNode        = errors.New("unknown node type")
	ErrMissingField       = errors.New("missing required field")
	ErrDuplicateField     = errors.New("duplicate field")
	ErrExtraArgs          = errors.New("too many arguments")
	ErrTypeMismatch       = errors.New("argument type mismatch")
	ErrUnknownProp        = errors.New("unknown property")
	ErrUnexpectedChildren = errors.New("unexpected child nodes")
	ErrAmbiguousFile      = errors.New("ambiguous file: contains both pipeline and fragment nodes")
	ErrEmptyInclude       = errors.New("no pipeline or fragment node found")
	ErrNilResolver        = errors.New("Parser.Resolver is nil")
)

// Parser converts KDL documents into validated pipeline definitions.
type Parser struct {
	// Resolver opens include sources during parsing. Must be non-nil before
	// calling ParseFile or ParseString.
	Resolver Resolver
}

// ParseFile reads and parses a KDL pipeline file at the given path.
func (p *Parser) ParseFile(path string) (pipelinemodel.Pipeline, error) {
	if p.Resolver == nil {
		return pipelinemodel.Pipeline{}, ErrNilResolver
	}
	rc, resolved, err := p.Resolver.Resolve(path, "")
	if err != nil {
		return pipelinemodel.Pipeline{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() { _ = rc.Close() }()

	return p.parse(rc, resolved)
}

// _syntheticFilename identifies in-memory content for dirOf fallback.
const _syntheticFilename = "<string>"

// _stdinFilename identifies content read from standard input.
const _stdinFilename = "<stdin>"

// ParseString parses KDL content from a string into a Pipeline.
// Includes are resolved relative to the current working directory.
func (p *Parser) ParseString(content string) (pipelinemodel.Pipeline, error) {
	return p.parse(strings.NewReader(content), _syntheticFilename)
}

// parse is the core parse entry: dispatches root nodes directly.
func (p *Parser) parse(r io.Reader, filename string) (pipelinemodel.Pipeline, error) {
	if p.Resolver == nil {
		return pipelinemodel.Pipeline{}, ErrNilResolver
	}
	doc, err := parseKDL(r, filename)
	if err != nil {
		return pipelinemodel.Pipeline{}, err
	}

	gc := newGroupCollector(filename)
	var acc pipelineAcc
	state := newIncludeState()

	for _, node := range doc.Nodes {
		if err := p.parsePipelineChild(node, filename, state, gc, &acc); err != nil {
			return pipelinemodel.Pipeline{}, err
		}
	}

	merged, err := gc.merge()
	if err != nil {
		return pipelinemodel.Pipeline{}, fmt.Errorf("%s: %w", filename, err)
	}

	return finalizePipeline(finalizePipelineInput{
		Name:     nameFromFilename(filename),
		Jobs:     merged,
		Env:      acc.Env,
		Secrets:  acc.Secrets,
		Matrix:   acc.Matrix,
		Defaults: acc.Defaults,
		Aliases:  state.aliases,
		Filename: filename,
	})
}

// pipelineAcc accumulates pipeline-level fields parsed from child nodes.
type pipelineAcc struct {
	Matrix   *pipelinemodel.Matrix
	Defaults *pipelinemodel.Defaults
	Env      []pipelinemodel.EnvVar
	Secrets  []pipelinemodel.SecretDecl
}

// parsePipelineChild dispatches a single child node of a pipeline block.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic parsePipelineChild is a flat switch dispatch; splitting it hurts readability.
func (p *Parser) parsePipelineChild(
	child *kdl.Node,
	filename string,
	state *includeState,
	gc *groupCollector,
	acc *pipelineAcc,
) error {
	switch nt := NodeType(child.Name()); nt {
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
	case NodeTypeSecret:
		sd, err := parseSecretDeclNode(child, filename, "pipeline")
		if err != nil {
			return err
		}
		acc.Secrets = append(acc.Secrets, sd)
	case NodeTypeInclude:
		jobs, inc, err := p.resolveChildInclude(child, filename, state)
		if err != nil {
			return err
		}
		gc.addInclude(jobs, inc)
	default:
		return fmt.Errorf(
			"%s: %w: %q (expected job, step, defaults, matrix, env, secret, or include)", filename, ErrUnknownNode, string(nt),
		)
	}
	return nil
}

// finalizePipelineInput groups parameters for finalizePipeline.
type finalizePipelineInput struct {
	Name     string
	Jobs     []pipelinemodel.Job
	Env      []pipelinemodel.EnvVar
	Secrets  []pipelinemodel.SecretDecl
	Matrix   *pipelinemodel.Matrix
	Defaults *pipelinemodel.Defaults
	Aliases  map[string][]string
	Filename string
}

// finalizePipeline applies defaults, expands aliases and matrices, then validates.
func finalizePipeline(in finalizePipelineInput) (pipelinemodel.Pipeline, error) {
	jobs, err := pipeline.ExpandAliases(in.Jobs, in.Aliases)
	if err != nil {
		return pipelinemodel.Pipeline{}, fmt.Errorf("%s: %w", in.Filename, err)
	}

	// Apply defaults before expansion and validation.
	jobs = pipeline.ApplyDefaults(jobs, in.Defaults)

	pl := pipelinemodel.Pipeline{
		Name:     in.Name,
		Jobs:     jobs,
		Env:      in.Env,
		Secrets:  in.Secrets,
		Matrix:   in.Matrix,
		Defaults: in.Defaults,
	}
	pl, err = pipeline.Expand(pl)
	if err != nil {
		return pipelinemodel.Pipeline{}, fmt.Errorf("%s: %w", in.Filename, err)
	}

	if _, err := pipeline.Validate(&pl); err != nil {
		return pipelinemodel.Pipeline{}, fmt.Errorf("%s: %w", in.Filename, err)
	}
	return pl, nil
}
