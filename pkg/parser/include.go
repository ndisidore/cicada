package parser

import (
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/sblinch/kdl-go/document"

	"github.com/ndisidore/cicada/pkg/pipeline"
)

// _maxIncludeDepth prevents runaway transitive includes.
const _maxIncludeDepth = 64

// includeState tracks ancestry for cycle detection, caches resolved fragments
// for diamond deduplication, and accumulates alias -> terminal step mappings.
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

// cacheKey produces a dedup key from absolute path and sorted param values so
// diamond includes with identical params resolve once.
func cacheKey(absPath string, params map[string]string) string {
	if len(params) == 0 {
		return absPath
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + params[k]
	}
	return absPath + ":" + strings.Join(pairs, ",")
}

// includeDirective holds the parsed properties of an include node.
type includeDirective struct {
	source     string
	alias      string
	onConflict pipeline.ConflictStrategy
	params     map[string]string
}

// groupCollector accumulates step groups in document order, flushing inline
// steps as a group boundary when an include is encountered.
type groupCollector struct {
	inline []pipeline.Step
	groups []pipeline.StepGroup
	origin string
}

func newGroupCollector(origin string) *groupCollector {
	return &groupCollector{origin: origin}
}

func (gc *groupCollector) addStep(s pipeline.Step) {
	gc.inline = append(gc.inline, s)
}

func (gc *groupCollector) addInclude(steps []pipeline.Step, inc includeDirective) {
	gc.flush()
	gc.groups = append(gc.groups, pipeline.StepGroup{
		Steps:      steps,
		Origin:     inc.source,
		OnConflict: inc.onConflict,
	})
}

func (gc *groupCollector) flush() {
	if len(gc.inline) > 0 {
		gc.groups = append(gc.groups, pipeline.StepGroup{
			Steps:  gc.inline,
			Origin: gc.origin,
		})
		gc.inline = nil
	}
}

func (gc *groupCollector) merge() ([]pipeline.Step, error) {
	gc.flush()
	return pipeline.MergeSteps(gc.groups)
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
			frag.Params = append(frag.Params, pd)
		case NodeTypeStep:
			step, err := parseStep(child, filename)
			if err != nil {
				return pipeline.Fragment{}, err
			}
			gc.addStep(step)
		case NodeTypeInclude:
			steps, inc, err := p.resolveChildInclude(child, filename, state)
			if err != nil {
				return pipeline.Fragment{}, err
			}
			gc.addInclude(steps, inc)
		default:
			return pipeline.Fragment{}, fmt.Errorf(
				"%s: fragment %q: %w: %q (expected param, step, or include)",
				filename, name, ErrUnknownNode, string(nt),
			)
		}
	}

	merged, err := gc.merge()
	if err != nil {
		return pipeline.Fragment{}, fmt.Errorf("%s: fragment %q: %w", filename, name, err)
	}
	frag.Steps = merged
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

// resolveChildInclude parses an include child node and resolves it.
func (p *Parser) resolveChildInclude(child *document.Node, filename string, state *includeState) ([]pipeline.Step, includeDirective, error) {
	inc, err := parseIncludeNode(child, filename)
	if err != nil {
		return nil, includeDirective{}, err
	}
	steps, err := p.resolveInclude(&inc, filename, state)
	if err != nil {
		return nil, includeDirective{}, err
	}
	return steps, inc, nil
}

// resolveInclude resolves a single include directive into a slice of steps,
// handling fragment params, cycle detection, and diamond dedup. The alias
// field of inc is filled from the included file's name when left empty.
func (p *Parser) resolveInclude(inc *includeDirective, fromFile string, state *includeState) ([]pipeline.Step, error) {
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
		if _, exists := state.aliases[inc.alias]; exists {
			return nil, fmt.Errorf(
				"%s: include %q: alias %q: %w",
				fromFile, inc.source, inc.alias, pipeline.ErrDuplicateAlias,
			)
		}
		terminals := pipeline.TerminalSteps(cached.Steps)
		state.aliases[inc.alias] = terminals
		return cached.Steps, nil
	}

	name, steps, err := p.parseIncludedFile(rc, absPath, inc.params, state)
	if err != nil {
		return nil, fmt.Errorf("%s: include %q: %w", fromFile, inc.source, err)
	}

	if inc.alias == "" {
		inc.alias = name
	}
	if _, exists := state.aliases[inc.alias]; exists {
		return nil, fmt.Errorf(
			"%s: include %q: alias %q: %w",
			fromFile, inc.source, inc.alias, pipeline.ErrDuplicateAlias,
		)
	}

	terminals := pipeline.TerminalSteps(steps)
	state.aliases[inc.alias] = terminals
	return steps, nil
}

// parseIncludedFile parses an included file (either pipeline or fragment) and
// returns the fragment/pipeline name along with its steps (params substituted).
func (p *Parser) parseIncludedFile(rc io.Reader, absPath string, params map[string]string, state *includeState) (string, []pipeline.Step, error) {
	doc, err := parseKDL(rc, absPath)
	if err != nil {
		return "", nil, err
	}

	// Detect whether this is a pipeline or fragment file.
	var pipelineNode, fragNode *document.Node
	for _, node := range doc.Nodes {
		switch nt := NodeType(node.Name.ValueString()); nt {
		case NodeTypePipeline:
			pipelineNode = node
		case NodeTypeFragment:
			fragNode = node
		default:
			return "", nil, fmt.Errorf("%s: %w: %q (expected pipeline or fragment)", absPath, ErrUnknownNode, string(nt))
		}
	}

	switch {
	case fragNode != nil && pipelineNode != nil:
		return "", nil, fmt.Errorf("%s: %w", absPath, ErrAmbiguousFile)
	case fragNode != nil:
		return p.includeFragment(fragNode, absPath, params, state)
	case pipelineNode != nil:
		return p.includePipeline(pipelineNode, absPath, params, state)
	default:
		return "", nil, fmt.Errorf("%s: no pipeline or fragment node found", absPath)
	}
}

// includeFragment resolves a fragment node from an included file.
func (p *Parser) includeFragment(node *document.Node, absPath string, params map[string]string, state *includeState) (string, []pipeline.Step, error) {
	frag, err := p.parseFragmentNode(node, absPath, state)
	if err != nil {
		return "", nil, err
	}
	resolved, err := pipeline.ResolveParams(frag.Params, params)
	if err != nil {
		return "", nil, fmt.Errorf("fragment %q: %w", frag.Name, err)
	}
	steps := pipeline.SubstituteParams(frag.Steps, resolved)
	state.cache[cacheKey(absPath, params)] = pipeline.Fragment{
		Name:   frag.Name,
		Params: frag.Params,
		Steps:  steps,
	}
	return frag.Name, steps, nil
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

// includePipeline extracts steps from an included pipeline file, ignoring its
// pipeline-level matrix.
func (p *Parser) includePipeline(node *document.Node, absPath string, params map[string]string, state *includeState) (string, []pipeline.Step, error) {
	name, err := requireStringArg(node, absPath, string(NodeTypePipeline))
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", absPath, err)
	}
	if len(params) > 0 {
		return "", nil, fmt.Errorf("pipeline %q does not accept parameters", name)
	}

	var steps []pipeline.Step
	for _, child := range node.Children {
		switch nt := NodeType(child.Name.ValueString()); nt {
		case NodeTypeStep:
			step, err := parseStep(child, absPath)
			if err != nil {
				return "", nil, err
			}
			steps = append(steps, step)
		case NodeTypeMatrix:
			slog.Warn("included pipeline matrix ignored",
				slog.String("pipeline", name),
				slog.String("file", absPath),
			)
		case NodeTypeInclude:
			incSteps, _, err := p.resolveChildInclude(child, absPath, state)
			if err != nil {
				return "", nil, err
			}
			steps = append(steps, incSteps...)
		default:
			return "", nil, fmt.Errorf("%s: %w: %q", absPath, ErrUnknownNode, string(nt))
		}
	}
	state.cache[cacheKey(absPath, params)] = pipeline.Fragment{
		Name:  name,
		Steps: steps,
	}
	return name, steps, nil
}
