//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"fmt"
	"time"

	kdl "github.com/calico32/kdl-go"

	"github.com/ndisidore/cicada/pkg/conditional"
	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// parseJob parses a job KDL node into a pipelinemodel.Job.
func parseJob(node *kdl.Node, filename string) (pipelinemodel.Job, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeJob))
	if err != nil {
		return pipelinemodel.Job{}, fmt.Errorf(
			"%s: job missing name: %w: %w", filename, ErrMissingField, err,
		)
	}

	j := pipelinemodel.Job{Name: name}
	seen := make(map[NodeType]bool)
	for _, child := range childrenOf(node) {
		if err := applyJobField(&j, child, filename, seen); err != nil {
			return pipelinemodel.Job{}, err
		}
	}
	return j, nil
}

// applyJobField dispatches a single child node of a job block.
// seen tracks fields whose zero value is valid (e.g. timeout "0s") so
// duplicate declarations are detected even when the parsed value equals
// the type's zero value.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic,function-length applyJobField is a flat switch dispatch; splitting it hurts readability.
func applyJobField(j *pipelinemodel.Job, node *kdl.Node, filename string, seen map[NodeType]bool) error {
	nt := NodeType(node.Name())
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
		j.Mounts = append(j.Mounts, pipelinemodel.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
	case NodeTypeCache:
		c, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		j.Caches = append(j.Caches, pipelinemodel.Cache{ID: c[0], Target: c[1]})
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
		if len(node.Arguments()) > 0 {
			return fmt.Errorf("%s: %s: no-cache takes no arguments: %w", filename, scope, ErrExtraArgs)
		}
		if j.NoCache {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		j.NoCache = true
	case NodeTypePublish:
		pub, err := parsePublishNode(node, filename, j.Name)
		if err != nil {
			return err
		}
		return setOnce(&j.Publish, &pub, filename, scope, string(nt))
	case NodeTypeWhen:
		w, err := parseWhenNode(node, filename, scope)
		if err != nil {
			return err
		}
		return setOnce(&j.When, w, filename, scope, string(nt))
	case NodeTypeTimeout:
		if seen[nt] {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: %s: timeout: %w", filename, scope, err)
		}
		if d < 0 {
			return fmt.Errorf("%s: %s: timeout: %w", filename, scope, pipelinemodel.ErrNegativeTimeout)
		}
		j.Timeout = d
		seen[nt] = true
		return nil
	case NodeTypeRetry:
		r, err := parseRetryNode(node, filename, scope)
		if err != nil {
			return err
		}
		return setOnce(&j.Retry, r, filename, scope, string(nt))
	case NodeTypeShell:
		if j.Shell != nil {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		s, err := parseShellNode(node, filename, scope)
		if err != nil {
			return err
		}
		j.Shell = s
	case NodeTypeSecret:
		ref, err := parseSecretRefNode(node, filename, scope)
		if err != nil {
			return err
		}
		j.Secrets = append(j.Secrets, ref)
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

// parseJobStep parses a step KDL node within a job into a pipelinemodel.Step.
func parseJobStep(node *kdl.Node, filename, jobName string) (pipelinemodel.Step, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeStep))
	if err != nil {
		return pipelinemodel.Step{}, fmt.Errorf(
			"%s: job %q: step missing name: %w: %w", filename, jobName, ErrMissingField, err,
		)
	}

	s := pipelinemodel.Step{Name: name}
	seen := make(map[NodeType]bool)
	for _, child := range childrenOf(node) {
		if err := applyJobStepField(&s, child, filename, jobName, seen); err != nil {
			return pipelinemodel.Step{}, err
		}
	}
	return s, nil
}

// applyJobStepField dispatches a single child node of a step block within a job.
// seen tracks fields whose zero value is valid (e.g. timeout "0s") so
// duplicate declarations are detected even when the parsed value equals
// the type's zero value.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic,function-length applyJobStepField is a flat switch dispatch over node types; splitting it hurts readability.
func applyJobStepField(s *pipelinemodel.Step, node *kdl.Node, filename, jobName string, seen map[NodeType]bool) error {
	nt := NodeType(node.Name())
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
		s.Mounts = append(s.Mounts, pipelinemodel.Mount{Source: m[0], Target: m[1], ReadOnly: ro})
	case NodeTypeCache:
		c, err := stringArgs2(node, filename, string(nt))
		if err != nil {
			return err
		}
		s.Caches = append(s.Caches, pipelinemodel.Cache{ID: c[0], Target: c[1]})
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
	case NodeTypeWhen:
		w, err := parseWhenNode(node, filename, scope)
		if err != nil {
			return err
		}
		if w.Deferred {
			return fmt.Errorf("%s: %s: when: %w", filename, scope, conditional.ErrDeferredInStep)
		}
		return setOnce(&s.When, w, filename, scope, string(nt))
	case NodeTypeTimeout:
		if seen[nt] {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		v, err := requireStringArg(node, filename, string(nt))
		if err != nil {
			return err
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: %s: timeout: %w", filename, scope, err)
		}
		if d < 0 {
			return fmt.Errorf("%s: %s: timeout: %w", filename, scope, pipelinemodel.ErrNegativeTimeout)
		}
		s.Timeout = d
		seen[nt] = true
		return nil
	case NodeTypeShell:
		if s.Shell != nil {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		sh, err := parseShellNode(node, filename, scope)
		if err != nil {
			return err
		}
		s.Shell = sh
	case NodeTypeSecret:
		ref, err := parseSecretRefNode(node, filename, scope)
		if err != nil {
			return err
		}
		s.Secrets = append(s.Secrets, ref)
	case NodeTypeNoCache:
		if len(node.Arguments()) > 0 {
			return fmt.Errorf("%s: %s: no-cache takes no arguments: %w", filename, scope, ErrExtraArgs)
		}
		if s.NoCache {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		s.NoCache = true
	case NodeTypeAllowFailure:
		if len(node.Arguments()) > 0 {
			return fmt.Errorf("%s: %s: allow-failure takes no arguments: %w", filename, scope, ErrExtraArgs)
		}
		if s.AllowFailure {
			return fmt.Errorf("%s: %s: %w: %q", filename, scope, ErrDuplicateField, string(nt))
		}
		s.AllowFailure = true
	case NodeTypeRetry:
		r, err := parseRetryNode(node, filename, scope)
		if err != nil {
			return err
		}
		return setOnce(&s.Retry, r, filename, scope, string(nt))
	default:
		return fmt.Errorf(
			"%s: %s: %w: %q", filename, scope, ErrUnknownNode, string(nt),
		)
	}
	return nil
}

// parseBareStep parses a step node directly under a pipeline into a single-step
// Job. Job-level fields (image, platform, depends-on, etc.) go to the Job;
// run commands go to the inner Step.
//
//revive:disable-next-line:cognitive-complexity,cyclomatic parseBareStep is a flat switch dispatch over node types; splitting it hurts readability.
func parseBareStep(node *kdl.Node, filename string) (pipelinemodel.Job, error) {
	name, err := requireStringArg(node, filename, string(NodeTypeStep))
	if err != nil {
		return pipelinemodel.Job{}, fmt.Errorf(
			"%s: step missing name: %w: %w", filename, ErrMissingField, err,
		)
	}

	j := pipelinemodel.Job{Name: name}
	s := pipelinemodel.Step{Name: name}
	seen := make(map[NodeType]bool)

	for _, child := range childrenOf(node) {
		nt := NodeType(child.Name())
		switch nt {
		case NodeTypeRun:
			// run goes to the inner step.
			v, err := requireStringArg(child, filename, string(nt))
			if err != nil {
				return pipelinemodel.Job{}, err
			}
			s.Run = append(s.Run, v)
		case NodeTypeAllowFailure:
			if len(child.Arguments()) > 0 {
				return pipelinemodel.Job{}, fmt.Errorf(
					"%s: step %q: allow-failure takes no arguments: %w", filename, name, ErrExtraArgs)
			}
			if s.AllowFailure {
				return pipelinemodel.Job{}, fmt.Errorf(
					"%s: step %q: %w: %q", filename, name, ErrDuplicateField, string(nt))
			}
			s.AllowFailure = true
		case NodeTypeRetry:
			scope := fmt.Sprintf("step %q", name)
			r, err := parseRetryNode(child, filename, scope)
			if err != nil {
				return pipelinemodel.Job{}, err
			}
			if err := setOnce(&s.Retry, r, filename, scope, string(nt)); err != nil {
				return pipelinemodel.Job{}, err
			}
		case NodeTypeStep:
			return pipelinemodel.Job{}, fmt.Errorf(
				"%s: step %q: %w: nested %q (use job for multi-step)",
				filename, name, ErrUnknownNode, string(nt),
			)
		default:
			// All other fields go to the job.
			if err := applyJobField(&j, child, filename, seen); err != nil {
				return pipelinemodel.Job{}, err
			}
		}
	}

	j.Steps = []pipelinemodel.Step{s}
	return j, nil
}
