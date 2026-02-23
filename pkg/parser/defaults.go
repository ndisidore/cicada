//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"fmt"

	"github.com/sblinch/kdl-go/document"

	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// parseDefaults parses a defaults KDL node into a pipelinemodel.Defaults.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic parseDefaults is a flat switch dispatch over child node types; splitting it hurts readability.
func parseDefaults(node *document.Node, filename string) (pipelinemodel.Defaults, error) {
	if len(node.Arguments) > 0 {
		return pipelinemodel.Defaults{}, fmt.Errorf(
			"%s: defaults takes no arguments: %w", filename, ErrExtraArgs,
		)
	}
	var d pipelinemodel.Defaults
	for _, child := range node.Children {
		nt := NodeType(child.Name.ValueString())
		switch nt {
		case NodeTypeImage:
			v, err := requireStringArg(child, filename, string(nt))
			if err != nil {
				return pipelinemodel.Defaults{}, err
			}
			if err := setOnce(&d.Image, v, filename, "defaults", string(nt)); err != nil {
				return pipelinemodel.Defaults{}, err
			}
		case NodeTypeWorkdir:
			v, err := requireStringArg(child, filename, string(nt))
			if err != nil {
				return pipelinemodel.Defaults{}, err
			}
			if err := setOnce(&d.Workdir, v, filename, "defaults", string(nt)); err != nil {
				return pipelinemodel.Defaults{}, err
			}
		case NodeTypeMount:
			m, err := stringArgs2(child, filename, string(nt))
			if err != nil {
				return pipelinemodel.Defaults{}, err
			}
			ro, err := prop[bool](child, PropReadonly)
			if err != nil {
				return pipelinemodel.Defaults{}, fmt.Errorf("%s: defaults: mount: %w", filename, err)
			}
			d.Mounts = append(d.Mounts, pipelinemodel.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
		case NodeTypeEnv:
			ev, err := parseEnvNode(child, filename, "defaults")
			if err != nil {
				return pipelinemodel.Defaults{}, err
			}
			d.Env = append(d.Env, ev)
		case NodeTypeShell:
			if d.Shell != nil {
				return pipelinemodel.Defaults{}, fmt.Errorf("%s: defaults: %w: %q", filename, ErrDuplicateField, string(nt))
			}
			s, err := parseShellNode(child, filename, "defaults")
			if err != nil {
				return pipelinemodel.Defaults{}, err
			}
			d.Shell = s
		default:
			return pipelinemodel.Defaults{}, fmt.Errorf(
				"%s: defaults: %w: %q (expected image, workdir, mount, env, or shell)",
				filename, ErrUnknownNode, string(nt),
			)
		}
	}
	return d, nil
}
