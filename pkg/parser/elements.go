//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	kdl "github.com/calico32/kdl-go"

	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/pipeline"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// parseMatrix parses a matrix block into a pipelinemodel.Matrix. Each child node
// is a dimension where the node name is the dimension name and arguments are
// the string values. Structural validation (empty matrix, duplicate/invalid
// dimension names, empty dimensions, combination cap) is delegated to
// pipeline.ValidateMatrix.
func parseMatrix(node *kdl.Node, filename, scope string) (pipelinemodel.Matrix, error) {
	children := childrenOf(node)
	m := pipelinemodel.Matrix{
		Dimensions: make([]pipelinemodel.Dimension, 0, len(children)),
	}

	for _, child := range children {
		dimName := child.Name()
		childArgs := child.Arguments()
		values := make([]string, 0, len(childArgs))
		for i := range childArgs {
			v, err := arg[string](child, i)
			if err != nil {
				return pipelinemodel.Matrix{}, fmt.Errorf(
					"%s: %s: matrix dimension %q value %d: %w",
					filename, scope, dimName, i, err,
				)
			}
			values = append(values, v)
		}
		m.Dimensions = append(m.Dimensions, pipelinemodel.Dimension{Name: dimName, Values: values})
	}

	if err := pipeline.ValidateMatrix(&m); err != nil {
		return pipelinemodel.Matrix{}, fmt.Errorf("%s: %s: matrix: %w", filename, scope, err)
	}

	return m, nil
}

// parseEnvNode extracts a key-value pair from an env node (two string args).
func parseEnvNode(node *kdl.Node, filename, scope string) (pipelinemodel.EnvVar, error) {
	kv, err := stringArgs2(node, filename, string(NodeTypeEnv))
	if err != nil {
		return pipelinemodel.EnvVar{}, fmt.Errorf("%s: %s: %w", filename, scope, err)
	}
	return pipelinemodel.EnvVar{Key: kv[0], Value: kv[1]}, nil
}

// parseExportNode extracts an export path and required local property.
func parseExportNode(node *kdl.Node, filename, scopeName string) (pipelinemodel.Export, error) {
	if len(node.Arguments()) > 1 {
		return pipelinemodel.Export{}, fmt.Errorf(
			"%s: %q: export requires exactly one argument, got %d: %w",
			filename, scopeName, len(node.Arguments()), ErrExtraArgs,
		)
	}
	path, err := requireStringArg(node, filename, string(NodeTypeExport))
	if err != nil {
		return pipelinemodel.Export{}, fmt.Errorf("%s: %q: export: %w", filename, scopeName, err)
	}
	local, err := prop[string](node, PropLocal)
	if err != nil {
		return pipelinemodel.Export{}, fmt.Errorf("%s: %q: export: %w", filename, scopeName, err)
	}
	if local == "" {
		return pipelinemodel.Export{}, fmt.Errorf(
			"%s: %q: export: local property: %w", filename, scopeName, ErrMissingField,
		)
	}
	return pipelinemodel.Export{Path: path, Local: local}, nil
}

// parseArtifactNode extracts an artifact definition (one positional arg + source/target properties).
func parseArtifactNode(node *kdl.Node, filename, scopeName string) (pipelinemodel.Artifact, error) {
	if len(node.Arguments()) > 1 {
		return pipelinemodel.Artifact{}, fmt.Errorf(
			"%s: %q: artifact requires exactly one argument, got %d: %w",
			filename, scopeName, len(node.Arguments()), ErrExtraArgs,
		)
	}
	from, err := requireStringArg(node, filename, string(NodeTypeArtifact))
	if err != nil {
		return pipelinemodel.Artifact{}, fmt.Errorf("%s: %q: artifact: %w", filename, scopeName, err)
	}
	source, err := prop[string](node, PropSource)
	if err != nil {
		return pipelinemodel.Artifact{}, fmt.Errorf("%s: %q: artifact: %w", filename, scopeName, err)
	}
	if source == "" {
		return pipelinemodel.Artifact{}, fmt.Errorf(
			"%s: %q: artifact: source property: %w", filename, scopeName, ErrMissingField,
		)
	}
	target, err := prop[string](node, PropTarget)
	if err != nil {
		return pipelinemodel.Artifact{}, fmt.Errorf("%s: %q: artifact: %w", filename, scopeName, err)
	}
	if target == "" {
		return pipelinemodel.Artifact{}, fmt.Errorf(
			"%s: %q: artifact: target property: %w", filename, scopeName, ErrMissingField,
		)
	}
	return pipelinemodel.Artifact{From: from, Source: source, Target: target}, nil
}

// parsePublishNode extracts a publish declaration from a KDL node.
// One required string argument (image reference), optional push bool prop
// (default true), optional insecure bool prop (default false).
func parsePublishNode(node *kdl.Node, filename, scopeName string) (pipelinemodel.Publish, error) {
	if len(node.Arguments()) > 1 {
		return pipelinemodel.Publish{}, fmt.Errorf(
			"%s: %q: publish requires exactly one argument, got %d: %w",
			filename, scopeName, len(node.Arguments()), ErrExtraArgs,
		)
	}
	image, err := requireStringArg(node, filename, string(NodeTypePublish))
	if err != nil {
		return pipelinemodel.Publish{}, fmt.Errorf("%s: %q: publish: %w", filename, scopeName, err)
	}
	push, err := prop[bool](node, PropPush)
	if err != nil {
		return pipelinemodel.Publish{}, fmt.Errorf("%s: %q: publish: %w", filename, scopeName, err)
	}
	insecure, err := prop[bool](node, PropInsecure)
	if err != nil {
		return pipelinemodel.Publish{}, fmt.Errorf("%s: %q: publish: %w", filename, scopeName, err)
	}
	// Default push=true when the property is absent.
	if _, hasPush := node.Properties()[PropPush]; !hasPush {
		push = true
	}
	return pipelinemodel.Publish{Image: image, Push: push, Insecure: insecure}, nil
}

// parseWhenNode extracts a when condition from a KDL node.
func parseWhenNode(node *kdl.Node, filename, scopeName string) (*conditional.When, error) {
	if len(node.Arguments()) > 1 {
		return nil, fmt.Errorf(
			"%s: %s: when requires exactly one argument, got %d: %w",
			filename, scopeName, len(node.Arguments()), ErrExtraArgs,
		)
	}
	expr, err := requireStringArg(node, filename, string(NodeTypeWhen))
	if err != nil {
		return nil, fmt.Errorf("%s: %s: when: %w", filename, scopeName, err)
	}
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("%s: %s: when expression: %w", filename, scopeName, ErrMissingField)
	}
	w, err := conditional.NewWhen(expr)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: when: %w", filename, scopeName, err)
	}
	return w, nil
}

// parseRetryNode parses a retry block into a *pipelinemodel.Retry.
// Children: attempts (int arg), delay (string arg, parsed as duration), backoff (string arg).
// All optional. Defaults: attempts=1, delay=0, backoff="none".
//
//revive:disable-next-line:cognitive-complexity,cyclomatic parseRetryNode is a linear field-dispatch parser; splitting it hurts readability.
func parseRetryNode(node *kdl.Node, filename, scope string) (*pipelinemodel.Retry, error) {
	r := &pipelinemodel.Retry{
		Attempts: 1,
		Backoff:  pipelinemodel.BackoffNone,
	}
	if len(node.Arguments()) > 0 {
		return nil, fmt.Errorf("%s: %s: retry takes no arguments: %w", filename, scope, ErrExtraArgs)
	}
	seen := make(map[string]bool, 3)
	for _, child := range childrenOf(node) {
		name := child.Name()
		if seen[name] {
			return nil, fmt.Errorf("%s: %s: retry: %w: %q", filename, scope, ErrDuplicateField, name)
		}
		seen[name] = true
		switch name {
		case PropAttempts:
			if len(child.Arguments()) > 1 {
				return nil, fmt.Errorf(
					"%s: %s: retry %s requires exactly one argument, got %d: %w",
					filename, scope, name, len(child.Arguments()), ErrExtraArgs,
				)
			}
			v, err := arg[int](child, 0)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: retry attempts: %w", filename, scope, err)
			}
			if v < 1 {
				return nil, fmt.Errorf("%s: %s: retry: attempts %d: %w", filename, scope, v, pipelinemodel.ErrInvalidRetryAttempts)
			}
			r.Attempts = v
		case PropDelay:
			if len(child.Arguments()) > 1 {
				return nil, fmt.Errorf(
					"%s: %s: retry %s requires exactly one argument, got %d: %w",
					filename, scope, name, len(child.Arguments()), ErrExtraArgs,
				)
			}
			v, err := requireStringArg(child, filename, name)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: retry delay: %w", filename, scope, err)
			}
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: retry delay: %w", filename, scope, err)
			}
			if d < 0 {
				return nil, fmt.Errorf("%s: %s: retry delay: %w", filename, scope, pipelinemodel.ErrNegativeDelay)
			}
			r.Delay = d
		case PropBackoff:
			if len(child.Arguments()) > 1 {
				return nil, fmt.Errorf(
					"%s: %s: retry %s requires exactly one argument, got %d: %w",
					filename, scope, name, len(child.Arguments()), ErrExtraArgs,
				)
			}
			v, err := requireStringArg(child, filename, name)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: retry backoff: %w", filename, scope, err)
			}
			bs := pipelinemodel.BackoffStrategy(v)
			switch bs {
			case pipelinemodel.BackoffNone, pipelinemodel.BackoffLinear, pipelinemodel.BackoffExponential:
			default:
				return nil, fmt.Errorf("%s: %s: retry backoff %q: %w", filename, scope, v, pipelinemodel.ErrInvalidBackoff)
			}
			r.Backoff = bs
		default:
			return nil, fmt.Errorf("%s: %s: retry: %w: %q", filename, scope, ErrUnknownNode, name)
		}
	}
	return r, nil
}

// parseShellNode parses variadic string arguments from a shell node.
func parseShellNode(node *kdl.Node, filename, scope string) ([]string, error) {
	nodeArgs := node.Arguments()
	if len(nodeArgs) == 0 {
		return nil, fmt.Errorf("%s: %s: shell: %w", filename, scope, pipelinemodel.ErrEmptyShell)
	}
	args := make([]string, 0, len(nodeArgs))
	for i := range nodeArgs {
		v, err := arg[string](node, i)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: shell argument %d: %w", filename, scope, i, err)
		}
		args = append(args, v)
	}
	return args, nil
}

// parseSecretDeclNode parses a pipeline-level secret declaration from a KDL node.
// Children: source (required), var (optional), path (optional), cmd (optional).
// Semantic validation (valid source values, conditional field requirements, var
// restriction to hostEnv) is deferred to pipeline.Validate via validateSecretDecls.
//
//revive:disable-next-line:cognitive-complexity parseSecretDeclNode is a linear field-dispatch parser; splitting it hurts readability.
func parseSecretDeclNode(node *kdl.Node, filename, scope string) (pipelinemodel.SecretDecl, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeSecret))
	if err != nil {
		return pipelinemodel.SecretDecl{}, fmt.Errorf("%s: %s: secret missing name: %w", filename, scope, err)
	}
	if len(node.Arguments()) > 1 {
		return pipelinemodel.SecretDecl{}, fmt.Errorf(
			"%s: %s: secret %q: requires exactly one argument, got %d: %w",
			filename, scope, name, len(node.Arguments()), ErrExtraArgs,
		)
	}
	// Secret declarations use children only (source, var, path, cmd); reject stray properties.
	if len(node.Properties()) > 0 {
		k := slices.Sorted(maps.Keys(node.Properties()))[0]
		return pipelinemodel.SecretDecl{}, fmt.Errorf(
			"%s: %s: secret %q: %w: %q", filename, scope, name, ErrUnknownProp, k,
		)
	}
	d := pipelinemodel.SecretDecl{Name: name}
	seen := make(map[string]bool, 4)
	for _, child := range childrenOf(node) {
		field := child.Name()
		if seen[field] {
			return pipelinemodel.SecretDecl{}, fmt.Errorf("%s: %s: secret %q: %w: %q", filename, scope, name, ErrDuplicateField, field)
		}
		seen[field] = true
		switch field {
		case PropSource, PropVar, PropPath, PropCmd:
			if len(child.Arguments()) > 1 {
				return pipelinemodel.SecretDecl{}, fmt.Errorf(
					"%s: %s: secret %q: %s requires exactly one argument, got %d: %w",
					filename, scope, name, field, len(child.Arguments()), ErrExtraArgs,
				)
			}
			if len(child.Properties()) > 0 {
				k := slices.Sorted(maps.Keys(child.Properties()))[0]
				return pipelinemodel.SecretDecl{}, fmt.Errorf(
					"%s: %s: secret %q: %s: %w: %q", filename, scope, name, field, ErrUnknownProp, k,
				)
			}
			v, err := requireStringArg(child, filename, field)
			if err != nil {
				return pipelinemodel.SecretDecl{}, fmt.Errorf("%s: %s: secret %q: %s: %w", filename, scope, name, field, err)
			}
			switch field {
			case PropSource:
				d.Source = pipelinemodel.SecretSource(v)
			case PropVar:
				d.Var = v
			case PropPath:
				d.Path = v
			default: // PropCmd (guarded by outer case)
				d.Cmd = v
			}
		default:
			return pipelinemodel.SecretDecl{}, fmt.Errorf("%s: %s: secret %q: %w: %q", filename, scope, name, ErrUnknownNode, field)
		}
	}
	if d.Source == "" {
		return pipelinemodel.SecretDecl{}, fmt.Errorf("%s: %s: secret %q: source: %w", filename, scope, name, ErrMissingField)
	}
	return d, nil
}

// parseSecretRefNode parses a job/step-level secret reference from a KDL node.
// One required string argument (secret name), optional env= and mount= properties.
func parseSecretRefNode(node *kdl.Node, filename, scope string) (pipelinemodel.SecretRef, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeSecret))
	if err != nil {
		return pipelinemodel.SecretRef{}, fmt.Errorf("%s: %s: secret ref missing name: %w", filename, scope, err)
	}
	if len(node.Arguments()) > 1 {
		return pipelinemodel.SecretRef{}, fmt.Errorf(
			"%s: %s: secret %q: requires exactly one argument, got %d: %w",
			filename, scope, name, len(node.Arguments()), ErrExtraArgs,
		)
	}
	if len(childrenOf(node)) > 0 {
		return pipelinemodel.SecretRef{}, fmt.Errorf(
			"%s: %s: secret %q: %w", filename, scope, name, ErrUnexpectedChildren,
		)
	}
	envVal, err := prop[string](node, PropEnv)
	if err != nil {
		return pipelinemodel.SecretRef{}, fmt.Errorf("%s: %s: secret %q: %w", filename, scope, name, err)
	}
	mountVal, err := prop[string](node, PropMount)
	if err != nil {
		return pipelinemodel.SecretRef{}, fmt.Errorf("%s: %s: secret %q: %w", filename, scope, name, err)
	}
	// Reject unknown properties deterministically.
	for _, k := range slices.Sorted(maps.Keys(node.Properties())) {
		if k != PropEnv && k != PropMount {
			return pipelinemodel.SecretRef{}, fmt.Errorf(
				"%s: %s: secret %q: %w: %q", filename, scope, name, ErrUnknownProp, k,
			)
		}
	}
	return pipelinemodel.SecretRef{Name: name, Env: envVal, Mount: mountVal}, nil
}
