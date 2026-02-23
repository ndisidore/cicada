package pipeline

import (
	"cmp"
	"fmt"
	"regexp"
	"strings"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// _posixIdentifier matches POSIX/C identifier names: starts with a letter
// or underscore, followed by zero or more letters, digits, or underscores.
var _posixIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateSecretDecls checks pipeline-level secret declarations for
// name uniqueness, valid sources, and source-specific field requirements.
//
//revive:disable-next-line:cognitive-complexity validateSecretDecls is a linear validation loop; splitting it hurts readability.
func validateSecretDecls(scope string, decls []pm.SecretDecl) error {
	seen := make(map[string]struct{}, len(decls))
	for _, d := range decls {
		if d.Name == "" {
			return fmt.Errorf("%s: %w", scope, pm.ErrEmptySecretName)
		}
		if _, ok := seen[d.Name]; ok {
			return fmt.Errorf("%s: secret %q: %w", scope, d.Name, pm.ErrDuplicateSecret)
		}
		seen[d.Name] = struct{}{}

		switch d.Source {
		case pm.SecretSourceHostEnv:
			envName := cmp.Or(d.Var, d.Name)
			if !_posixIdentifier.MatchString(envName) {
				return fmt.Errorf("%s: secret %q: env name %q: %w", scope, d.Name, envName, pm.ErrInvalidSecretHostEnvName)
			}
		case pm.SecretSourceFile:
			if strings.TrimSpace(d.Path) == "" {
				return fmt.Errorf("%s: secret %q: %w", scope, d.Name, pm.ErrMissingSecretPath)
			}
		case pm.SecretSourceCmd:
			if strings.TrimSpace(d.Cmd) == "" {
				return fmt.Errorf("%s: secret %q: %w", scope, d.Name, pm.ErrMissingSecretCmd)
			}
		default:
			return fmt.Errorf("%s: secret %q: source %q: %w", scope, d.Name, d.Source, pm.ErrInvalidSecretSource)
		}

		if err := validateStrayFields(d); err != nil {
			return fmt.Errorf("%s: secret %q: %w", scope, d.Name, err)
		}
	}
	return nil
}

// validateStrayFields rejects fields set on the wrong source type.
func validateStrayFields(d pm.SecretDecl) error {
	if d.Var != "" && d.Source != pm.SecretSourceHostEnv {
		return pm.ErrSecretVarWithNonHostEnv
	}
	if d.Path != "" && d.Source != pm.SecretSourceFile {
		return pm.ErrSecretPathWithNonFile
	}
	if d.Cmd != "" && d.Source != pm.SecretSourceCmd {
		return pm.ErrSecretCmdWithNonCmd
	}
	return nil
}

// validateSecretRefs checks job/step-level secret references for valid
// targets and that each references a declared secret. Returns the env/mount
// target sets so callers can carry them into child scopes.
func validateSecretRefs(scope string, refs []pm.SecretRef, declared map[string]struct{}) (envUsed, mountUsed map[string]struct{}, _ error) {
	return validateSecretRefsWithUsed(scope, refs, declared, nil, nil)
}

// validateSecretRefsWithUsed is the inner implementation of validateSecretRefs
// that accepts pre-populated env/mount tracking maps from a parent scope.
// Non-nil parentEnv/parentMount maps are mutated in-place to record used targets.
func validateSecretRefsWithUsed(
	scope string,
	refs []pm.SecretRef,
	declared map[string]struct{},
	parentEnv, parentMount map[string]struct{},
) (envUsed, mountUsed map[string]struct{}, _ error) {
	envUsed = parentEnv
	if envUsed == nil {
		envUsed = make(map[string]struct{}, len(refs))
	}
	mountUsed = parentMount
	if mountUsed == nil {
		mountUsed = make(map[string]struct{}, len(refs))
	}
	seen := make(map[string]struct{}, len(refs))
	for _, r := range refs {
		if r.Name == "" {
			return nil, nil, fmt.Errorf("%s: %w", scope, pm.ErrEmptySecretName)
		}
		if _, ok := declared[r.Name]; !ok {
			return nil, nil, fmt.Errorf("%s: secret %q: %w", scope, r.Name, pm.ErrUnknownSecret)
		}
		if _, ok := seen[r.Name]; ok {
			return nil, nil, fmt.Errorf("%s: secret %q: %w", scope, r.Name, pm.ErrDuplicateSecretRef)
		}
		seen[r.Name] = struct{}{}

		if err := validateRefTarget(r, envUsed, mountUsed); err != nil {
			return nil, nil, fmt.Errorf("%s: secret %q: %w", scope, r.Name, err)
		}
	}
	return envUsed, mountUsed, nil
}

// validateRefTarget checks a single SecretRef's env/mount target for validity
// and uniqueness, updating the envUsed/mountUsed tracking maps.
func validateRefTarget(r pm.SecretRef, envUsed, mountUsed map[string]struct{}) error {
	hasEnv := r.Env != ""
	hasMount := r.Mount != ""
	switch {
	case !hasEnv && !hasMount:
		return pm.ErrSecretRefNoTarget
	case hasEnv && hasMount:
		return pm.ErrSecretRefBothTarget
	case hasEnv && !_posixIdentifier.MatchString(r.Env):
		return fmt.Errorf("env %q: %w", r.Env, pm.ErrInvalidSecretEnvName)
	case hasMount && !strings.HasPrefix(r.Mount, "/"):
		return fmt.Errorf("mount %q: %w", r.Mount, pm.ErrRelativeSecretMount)
	}
	if hasEnv {
		if _, ok := envUsed[r.Env]; ok {
			return fmt.Errorf("env %q: %w", r.Env, pm.ErrDuplicateSecretEnvName)
		}
		envUsed[r.Env] = struct{}{}
	}
	if hasMount {
		if _, ok := mountUsed[r.Mount]; ok {
			return fmt.Errorf("mount %q: %w", r.Mount, pm.ErrDuplicateSecretMount)
		}
		mountUsed[r.Mount] = struct{}{}
	}
	return nil
}

// validateSecretInheritance checks that step-level secrets do not override
// (i.e. re-reference) secrets already referenced at the job level.
func validateSecretInheritance(scope string, jobRefs, stepRefs []pm.SecretRef) error {
	jobSet := make(map[string]struct{}, len(jobRefs))
	for _, r := range jobRefs {
		jobSet[r.Name] = struct{}{}
	}
	for _, r := range stepRefs {
		if _, ok := jobSet[r.Name]; ok {
			return fmt.Errorf("%s: secret %q: %w", scope, r.Name, pm.ErrSecretOverride)
		}
	}
	return nil
}

// secretDeclSet builds a lookup set from secret declarations.
func secretDeclSet(decls []pm.SecretDecl) map[string]struct{} {
	s := make(map[string]struct{}, len(decls))
	for _, d := range decls {
		s[d.Name] = struct{}{}
	}
	return s
}
