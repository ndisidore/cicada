//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"

	"github.com/sblinch/kdl-go/document"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// _maxIncludeDepth prevents runaway transitive includes.
const _maxIncludeDepth = 64

// includeState tracks ancestry for cycle detection, caches resolved fragments
// for diamond deduplication, and accumulates alias -> terminal job mappings.
type includeState struct {
	ancestors   []string
	ancestorSet map[string]struct{}
	cache       map[string]pipeline.Fragment
	aliases     map[string][]string
}

func newIncludeState() *includeState {
	return &includeState{
		ancestorSet: make(map[string]struct{}),
		cache:       make(map[string]pipeline.Fragment),
		aliases:     make(map[string][]string),
	}
}

// push adds a path to the ancestry stack, returning an error on cycles or
// depth overflow.
func (s *includeState) push(absPath string) error {
	if _, ok := s.ancestorSet[absPath]; ok {
		cycle := make([]string, len(s.ancestors)+1)
		copy(cycle, s.ancestors)
		cycle[len(s.ancestors)] = absPath
		return fmt.Errorf("%w: %s", pipeline.ErrCircularInclude, strings.Join(cycle, " -> "))
	}
	if len(s.ancestors) >= _maxIncludeDepth {
		return fmt.Errorf("%w: depth %d at %s", pipeline.ErrIncludeDepth, len(s.ancestors), absPath)
	}
	s.ancestors = append(s.ancestors, absPath)
	s.ancestorSet[absPath] = struct{}{}
	return nil
}

// pop removes the last path from the ancestry stack.
func (s *includeState) pop() {
	last := s.ancestors[len(s.ancestors)-1]
	s.ancestors = s.ancestors[:len(s.ancestors)-1]
	delete(s.ancestorSet, last)
}

// registerAlias records an alias -> terminal jobs mapping, returning
// ErrDuplicateAlias if the alias is already registered.
func (s *includeState) registerAlias(alias, fromFile, source string, terminals []string) error {
	if _, exists := s.aliases[alias]; exists {
		return fmt.Errorf(
			"%s: include %q: alias %q: %w",
			fromFile, source, alias, pipeline.ErrDuplicateAlias,
		)
	}
	s.aliases[alias] = terminals
	return nil
}

// cacheKey produces a dedup key from absolute path and sorted, percent-encoded
// param values so diamond includes with identical params resolve once.
func cacheKey(absPath string, params map[string]string) string {
	if len(params) == 0 {
		return absPath
	}
	vals := make(url.Values, len(params))
	for k, v := range params {
		vals.Set(k, v)
	}
	return absPath + "?" + vals.Encode()
}

// includeDirective holds the parsed properties of an include node.
type includeDirective struct {
	source     string
	alias      string
	onConflict pipeline.ConflictStrategy
	params     map[string]string
}

// groupCollector accumulates job groups in document order, flushing inline
// jobs as a group boundary when an include is encountered.
type groupCollector struct {
	inline []pipeline.Job
	groups []pipeline.JobGroup
	origin string
}

func newGroupCollector(origin string) *groupCollector {
	return &groupCollector{origin: origin}
}

func (gc *groupCollector) addJob(j pipeline.Job) {
	gc.inline = append(gc.inline, j)
}

func (gc *groupCollector) addInclude(jobs []pipeline.Job, inc includeDirective) {
	gc.flush()
	gc.groups = append(gc.groups, pipeline.JobGroup{
		Jobs:       jobs,
		Origin:     inc.source,
		OnConflict: inc.onConflict,
	})
}

func (gc *groupCollector) flush() {
	if len(gc.inline) > 0 {
		gc.groups = append(gc.groups, pipeline.JobGroup{
			Jobs:   gc.inline,
			Origin: gc.origin,
		})
		gc.inline = nil
	}
}

func (gc *groupCollector) merge() ([]pipeline.Job, error) {
	gc.flush()
	return pipeline.MergeJobs(gc.groups)
}

// parseIncludeNode extracts include properties from a KDL node.
func parseIncludeNode(node *document.Node, filename string) (includeDirective, error) {
	source, err := requireStringArg(node, filename, string(NodeTypeInclude))
	if err != nil {
		return includeDirective{}, err
	}

	alias, err := prop[string](node, PropAs)
	if err != nil {
		return includeDirective{}, fmt.Errorf("%s: include %q: %w", filename, source, err)
	}

	conflictStr, err := prop[string](node, PropOnConflict)
	if err != nil {
		return includeDirective{}, fmt.Errorf("%s: include %q: %w", filename, source, err)
	}
	conflict, err := parseConflictStrategy(conflictStr)
	if err != nil {
		return includeDirective{}, fmt.Errorf("%s: include %q: %w", filename, source, err)
	}

	// Reject unknown properties (only as + on-conflict allowed).
	for k := range node.Properties {
		if k != PropAs && k != PropOnConflict {
			return includeDirective{}, fmt.Errorf(
				"%s: include %q: %w: %q", filename, source, ErrUnknownProp, k,
			)
		}
	}

	// Parameters are passed as child nodes.
	params := make(map[string]string, len(node.Children))
	for _, child := range node.Children {
		name := child.Name.ValueString()
		if _, dup := params[name]; dup {
			return includeDirective{}, fmt.Errorf(
				"%s: include %q: %w: %q", filename, source, ErrDuplicateField, name,
			)
		}
		val, err := requireStringArg(child, filename, name)
		if err != nil {
			return includeDirective{}, fmt.Errorf(
				"%s: include %q: param %q: %w", filename, source, name, err,
			)
		}
		params[name] = val
	}

	return includeDirective{
		source:     source,
		alias:      alias,
		onConflict: conflict,
		params:     params,
	}, nil
}

// parseFragmentNode parses a fragment KDL node into a pipeline.Fragment.
//
//revive:disable-next-line:cognitive-complexity parseFragmentNode is a flat switch dispatch over child node types; splitting it hurts readability.
func (p *Parser) parseFragmentNode(node *document.Node, filename string, state *includeState) (pipeline.Fragment, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeFragment))
	if err != nil {
		return pipeline.Fragment{}, fmt.Errorf("%s: fragment missing name: %w", filename, err)
	}

	frag := pipeline.Fragment{
		Name:   name,
		Params: make([]pipeline.ParamDef, 0),
	}
	gc := newGroupCollector(filename)

	for _, child := range node.Children {
		switch nt := NodeType(child.Name.ValueString()); nt {
		case NodeTypeParam:
			pd, err := parseParamNode(child, filename)
			if err != nil {
				return pipeline.Fragment{}, err
			}
			if err := checkDuplicateParam(frag.Params, pd.Name, filename, name); err != nil {
				return pipeline.Fragment{}, err
			}
			frag.Params = append(frag.Params, pd)
		case NodeTypeStep:
			// Bare step sugar within fragment.
			job, err := parseBareStep(child, filename)
			if err != nil {
				return pipeline.Fragment{}, err
			}
			gc.addJob(job)
		case NodeTypeJob:
			job, err := parseJob(child, filename)
			if err != nil {
				return pipeline.Fragment{}, err
			}
			gc.addJob(job)
		case NodeTypeInclude:
			jobs, inc, err := p.resolveChildInclude(child, filename, state)
			if err != nil {
				return pipeline.Fragment{}, err
			}
			gc.addInclude(jobs, inc)
		default:
			return pipeline.Fragment{}, fmt.Errorf(
				"%s: fragment %q: %w: %q (expected param, step, job, or include)",
				filename, name, ErrUnknownNode, string(nt),
			)
		}
	}

	merged, err := gc.merge()
	if err != nil {
		return pipeline.Fragment{}, fmt.Errorf("%s: fragment %q: %w", filename, name, err)
	}
	frag.Jobs = merged
	return frag, nil
}

// parseParamNode parses a param KDL node into a pipeline.ParamDef.
func parseParamNode(node *document.Node, filename string) (pipeline.ParamDef, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeParam))
	if err != nil {
		return pipeline.ParamDef{}, fmt.Errorf("%s: param missing name: %w", filename, err)
	}

	def, err := prop[string](node, PropDefault)
	if err != nil {
		return pipeline.ParamDef{}, fmt.Errorf("%s: param %q: %w", filename, name, err)
	}

	pd := pipeline.ParamDef{Name: name}
	if _, hasDefault := node.Properties[PropDefault]; hasDefault {
		pd.Default = def
	} else {
		pd.Required = true
	}
	return pd, nil
}

// checkDuplicateParam returns ErrDuplicateParam if name already exists in defs.
func checkDuplicateParam(defs []pipeline.ParamDef, name, filename, fragName string) error {
	for _, existing := range defs {
		if existing.Name == name {
			return fmt.Errorf(
				"%s: fragment %q: param %q: %w",
				filename, fragName, name, pipeline.ErrDuplicateParam,
			)
		}
	}
	return nil
}

// resolveChildInclude parses an include child node and resolves it.
func (p *Parser) resolveChildInclude(child *document.Node, filename string, state *includeState) ([]pipeline.Job, includeDirective, error) {
	inc, err := parseIncludeNode(child, filename)
	if err != nil {
		return nil, includeDirective{}, err
	}
	// Pointer so resolveInclude can fill inc.alias from the included name.
	jobs, err := p.resolveInclude(&inc, filename, state)
	if err != nil {
		return nil, includeDirective{}, err
	}
	return jobs, inc, nil
}

// resolveInclude resolves a single include directive into a slice of jobs,
// handling fragment params, cycle detection, and diamond dedup. The alias
// field of inc is filled from the included file's name when left empty.
func (p *Parser) resolveInclude(inc *includeDirective, fromFile string, state *includeState) ([]pipeline.Job, error) {
	basePath := dirOf(fromFile)
	rc, absPath, err := p.Resolver.Resolve(inc.source, basePath)
	if err != nil {
		return nil, fmt.Errorf("%s: include %q: %w", fromFile, inc.source, err)
	}
	defer func() { _ = rc.Close() }()

	if err := state.push(absPath); err != nil {
		return nil, fmt.Errorf("%s: %w", fromFile, err)
	}
	defer state.pop()

	// Diamond dedup: check cache.
	key := cacheKey(absPath, inc.params)
	if cached, ok := state.cache[key]; ok {
		if inc.alias == "" {
			inc.alias = cached.Name
		}
		if err := state.registerAlias(inc.alias, fromFile, inc.source, pipeline.TerminalJobs(cached.Jobs)); err != nil {
			return nil, err
		}
		return cached.Jobs, nil
	}

	name, jobs, err := p.parseIncludedFile(rc, absPath, inc.params, state)
	if err != nil {
		return nil, fmt.Errorf("%s: include %q: %w", fromFile, inc.source, err)
	}

	if inc.alias == "" {
		inc.alias = name
	}
	if err := state.registerAlias(inc.alias, fromFile, inc.source, pipeline.TerminalJobs(jobs)); err != nil {
		return nil, err
	}
	return jobs, nil
}

// parseIncludedFile parses an included file (either pipeline or fragment) and
// returns the fragment/pipeline name along with its jobs (params substituted).
// A file containing a fragment node is a fragment file; a file with pipeline-child
// nodes (job, step, defaults, etc.) is a pipeline file. Both present → ambiguous.
func (p *Parser) parseIncludedFile(rc io.Reader, absPath string, params map[string]string, state *includeState) (string, []pipeline.Job, error) {
	doc, err := parseKDL(rc, absPath)
	if err != nil {
		return "", nil, err
	}

	var fragNode *document.Node
	hasPipelineChildren := false
	for _, node := range doc.Nodes {
		switch nt := NodeType(node.Name.ValueString()); nt {
		case NodeTypeFragment:
			if fragNode != nil {
				return "", nil, fmt.Errorf("%s: %w: multiple %q nodes", absPath, ErrAmbiguousFile, NodeTypeFragment)
			}
			fragNode = node
		case NodeTypeJob, NodeTypeStep, NodeTypeDefaults, NodeTypeMatrix, NodeTypeEnv, NodeTypeInclude:
			hasPipelineChildren = true
		default:
			return "", nil, fmt.Errorf("%s: %w: %q (expected fragment, job, step, defaults, matrix, env, or include)", absPath, ErrUnknownNode, string(nt))
		}
	}

	switch {
	case fragNode != nil && hasPipelineChildren:
		return "", nil, fmt.Errorf("%s: %w", absPath, ErrAmbiguousFile)
	case fragNode != nil:
		return p.includeFragment(fragNode, absPath, params, state)
	case hasPipelineChildren:
		return p.includePipelineFile(doc.Nodes, absPath, params, state)
	default:
		return "", nil, fmt.Errorf("%s: %w", absPath, ErrEmptyInclude)
	}
}

// includeFragment resolves a fragment node from an included file.
func (p *Parser) includeFragment(node *document.Node, absPath string, params map[string]string, state *includeState) (string, []pipeline.Job, error) {
	frag, err := p.parseFragmentNode(node, absPath, state)
	if err != nil {
		return "", nil, err
	}
	resolved, err := pipeline.ResolveParams(frag.Params, params)
	if err != nil {
		return "", nil, fmt.Errorf("fragment %q: %w", frag.Name, err)
	}
	jobs := pipeline.SubstituteParams(frag.Jobs, resolved)
	state.cache[cacheKey(absPath, params)] = pipeline.Fragment{
		Name:   frag.Name,
		Params: frag.Params,
		Jobs:   jobs,
	}
	return frag.Name, jobs, nil
}

// parseConflictStrategy converts a string to a ConflictStrategy, returning
// ErrInvalidConflict for unrecognized values. An empty string defaults to error.
func parseConflictStrategy(s string) (pipeline.ConflictStrategy, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "error":
		return pipeline.ConflictError, nil
	case "skip":
		return pipeline.ConflictSkip, nil
	default:
		return 0, fmt.Errorf("%w: %q (expected \"error\" or \"skip\")", pipeline.ErrInvalidConflict, s)
	}
}

// includePipelineFile extracts jobs from an included pipeline file, ignoring its
// pipeline-level matrix and defaults.
//
//revive:disable-next-line:cognitive-complexity includePipelineFile is a flat switch dispatch; splitting it hurts readability.
func (p *Parser) includePipelineFile(nodes []*document.Node, absPath string, params map[string]string, state *includeState) (string, []pipeline.Job, error) {
	name := nameFromFilename(absPath)
	if len(params) > 0 {
		return "", nil, fmt.Errorf("pipeline %q: %w", name, pipeline.ErrPipelineNoParams)
	}

	gc := newGroupCollector(absPath)
	for _, node := range nodes {
		switch nt := NodeType(node.Name.ValueString()); nt {
		case NodeTypeStep:
			job, err := parseBareStep(node, absPath)
			if err != nil {
				return "", nil, err
			}
			gc.addJob(job)
		case NodeTypeJob:
			job, err := parseJob(node, absPath)
			if err != nil {
				return "", nil, err
			}
			gc.addJob(job)
		case NodeTypeMatrix:
			dims := make([]string, len(node.Children))
			for i, d := range node.Children {
				dims[i] = d.Name.ValueString()
			}
			slog.Warn("included pipeline matrix ignored",
				slog.String("pipeline", name),
				slog.String("file", absPath),
				slog.Any("dimensions", dims),
			)
		case NodeTypeEnv:
			slog.Warn("included pipeline env ignored",
				slog.String("pipeline", name),
				slog.String("file", absPath),
			)
		case NodeTypeDefaults:
			slog.Warn("included pipeline defaults ignored",
				slog.String("pipeline", name),
				slog.String("file", absPath),
			)
		case NodeTypeInclude:
			incJobs, inc, err := p.resolveChildInclude(node, absPath, state)
			if err != nil {
				return "", nil, err
			}
			gc.addInclude(incJobs, inc)
		default:
			return "", nil, fmt.Errorf("%s: %w: %q", absPath, ErrUnknownNode, string(nt))
		}
	}
	jobs, err := gc.merge()
	if err != nil {
		return "", nil, fmt.Errorf("%s: pipeline %q: %w", absPath, name, err)
	}
	state.cache[cacheKey(absPath, params)] = pipeline.Fragment{
		Name: name,
		Jobs: jobs,
	}
	return name, jobs, nil
}
