package pipeline

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SecretSource identifies how a secret value is obtained.
type SecretSource string

// SecretSource values.
const (
	SecretSourceHostEnv SecretSource = "hostEnv"
	SecretSourceFile    SecretSource = "file"
	SecretSourceCmd     SecretSource = "cmd"
)

// SecretDecl is a pipeline-level secret declaration that describes where
// a secret value comes from. Var is an optional host env var override for
// hostEnv sources (defaults to Name when empty).
type SecretDecl struct {
	Name   string       // unique secret identifier
	Source SecretSource // hostEnv | file | cmd
	Var    string       // host env var name (hostEnv only; defaults to Name)
	Path   string       // host file path (file only)
	Cmd    string       // shell command (cmd only)
}

// SecretRef is a job/step-level reference that injects a declared secret
// into the container. Exactly one of Env or Mount must be set.
type SecretRef struct {
	Name  string // references a SecretDecl.Name
	Env   string // inject as environment variable with this name
	Mount string // inject as file at this absolute path
}

// Sentinel errors for secret validation.
var (
	ErrEmptySecretName          = errors.New("secret name is empty")
	ErrDuplicateSecret          = errors.New("duplicate secret declaration")
	ErrDuplicateSecretRef       = errors.New("duplicate secret reference")
	ErrInvalidSecretSource      = errors.New("invalid secret source")
	ErrMissingSecretPath        = errors.New("file secret requires path")
	ErrMissingSecretCmd         = errors.New("cmd secret requires cmd")
	ErrUnknownSecret            = errors.New("secret reference to undeclared secret")
	ErrSecretRefNoTarget        = errors.New("secret ref must specify env or mount")
	ErrSecretRefBothTarget      = errors.New("secret ref must not specify both env and mount")
	ErrRelativeSecretMount      = errors.New("secret mount path must be absolute")
	ErrSecretOverride           = errors.New("step secret overrides job secret")
	ErrSecretResolution         = errors.New("secret resolution")
	ErrSecretVarWithNonHostEnv  = errors.New("var is only valid with hostEnv source")
	ErrInvalidSecretEnvName     = errors.New("env name must match POSIX identifier syntax")
	ErrSecretPathWithNonFile    = errors.New("path is only valid with file source")
	ErrSecretCmdWithNonCmd      = errors.New("cmd is only valid with cmd source")
	ErrDuplicateSecretEnvName   = errors.New("duplicate secret env target")
	ErrDuplicateSecretMount     = errors.New("duplicate secret mount target")
	ErrInvalidSecretHostEnvName = errors.New("hostEnv lookup name must be a valid POSIX identifier (set var to override)")
)

// _posixIdentifier matches POSIX/C identifier names: starts with a letter
// or underscore, followed by zero or more letters, digits, or underscores.
var _posixIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateSecretDecls checks pipeline-level secret declarations for
// name uniqueness, valid sources, and source-specific field requirements.
//
//revive:disable-next-line:cognitive-complexity validateSecretDecls is a linear validation loop; splitting it hurts readability.
func validateSecretDecls(scope string, decls []SecretDecl) error {
	seen := make(map[string]struct{}, len(decls))
	for _, d := range decls {
		if d.Name == "" {
			return fmt.Errorf("%s: %w", scope, ErrEmptySecretName)
		}
		if _, ok := seen[d.Name]; ok {
			return fmt.Errorf("%s: secret %q: %w", scope, d.Name, ErrDuplicateSecret)
		}
		seen[d.Name] = struct{}{}

		switch d.Source {
		case SecretSourceHostEnv:
			envName := cmp.Or(d.Var, d.Name)
			if !_posixIdentifier.MatchString(envName) {
				return fmt.Errorf("%s: secret %q: env name %q: %w", scope, d.Name, envName, ErrInvalidSecretHostEnvName)
			}
		case SecretSourceFile:
			if strings.TrimSpace(d.Path) == "" {
				return fmt.Errorf("%s: secret %q: %w", scope, d.Name, ErrMissingSecretPath)
			}
		case SecretSourceCmd:
			if strings.TrimSpace(d.Cmd) == "" {
				return fmt.Errorf("%s: secret %q: %w", scope, d.Name, ErrMissingSecretCmd)
			}
		default:
			return fmt.Errorf("%s: secret %q: source %q: %w", scope, d.Name, d.Source, ErrInvalidSecretSource)
		}

		if err := validateStrayFields(d); err != nil {
			return fmt.Errorf("%s: secret %q: %w", scope, d.Name, err)
		}
	}
	return nil
}

// validateStrayFields rejects fields set on the wrong source type.
func validateStrayFields(d SecretDecl) error {
	if d.Var != "" && d.Source != SecretSourceHostEnv {
		return ErrSecretVarWithNonHostEnv
	}
	if d.Path != "" && d.Source != SecretSourceFile {
		return ErrSecretPathWithNonFile
	}
	if d.Cmd != "" && d.Source != SecretSourceCmd {
		return ErrSecretCmdWithNonCmd
	}
	return nil
}

// validateSecretRefs checks job/step-level secret references for valid
// targets and that each references a declared secret. Returns the env/mount
// target sets so callers can carry them into child scopes.
func validateSecretRefs(scope string, refs []SecretRef, declared map[string]struct{}) (envUsed, mountUsed map[string]struct{}, _ error) {
	return validateSecretRefsWithUsed(scope, refs, declared, nil, nil)
}

// validateSecretRefsWithUsed is the inner implementation of validateSecretRefs
// that accepts pre-populated env/mount tracking maps from a parent scope.
// Non-nil parentEnv/parentMount maps are mutated in-place to record used targets.
func validateSecretRefsWithUsed(
	scope string,
	refs []SecretRef,
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
			return nil, nil, fmt.Errorf("%s: %w", scope, ErrEmptySecretName)
		}
		if _, ok := declared[r.Name]; !ok {
			return nil, nil, fmt.Errorf("%s: secret %q: %w", scope, r.Name, ErrUnknownSecret)
		}
		if _, ok := seen[r.Name]; ok {
			return nil, nil, fmt.Errorf("%s: secret %q: %w", scope, r.Name, ErrDuplicateSecretRef)
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
func validateRefTarget(r SecretRef, envUsed, mountUsed map[string]struct{}) error {
	hasEnv := r.Env != ""
	hasMount := r.Mount != ""
	switch {
	case !hasEnv && !hasMount:
		return ErrSecretRefNoTarget
	case hasEnv && hasMount:
		return ErrSecretRefBothTarget
	case hasEnv && !_posixIdentifier.MatchString(r.Env):
		return fmt.Errorf("env %q: %w", r.Env, ErrInvalidSecretEnvName)
	case hasMount && !strings.HasPrefix(r.Mount, "/"):
		return fmt.Errorf("mount %q: %w", r.Mount, ErrRelativeSecretMount)
	}
	if hasEnv {
		if _, ok := envUsed[r.Env]; ok {
			return fmt.Errorf("env %q: %w", r.Env, ErrDuplicateSecretEnvName)
		}
		envUsed[r.Env] = struct{}{}
	}
	if hasMount {
		if _, ok := mountUsed[r.Mount]; ok {
			return fmt.Errorf("mount %q: %w", r.Mount, ErrDuplicateSecretMount)
		}
		mountUsed[r.Mount] = struct{}{}
	}
	return nil
}

// validateSecretInheritance checks that step-level secrets do not override
// (i.e. re-reference) secrets already referenced at the job level.
func validateSecretInheritance(scope string, jobRefs, stepRefs []SecretRef) error {
	jobSet := make(map[string]struct{}, len(jobRefs))
	for _, r := range jobRefs {
		jobSet[r.Name] = struct{}{}
	}
	for _, r := range stepRefs {
		if _, ok := jobSet[r.Name]; ok {
			return fmt.Errorf("%s: secret %q: %w", scope, r.Name, ErrSecretOverride)
		}
	}
	return nil
}

// secretDeclSet builds a lookup set from secret declarations.
func secretDeclSet(decls []SecretDecl) map[string]struct{} {
	s := make(map[string]struct{}, len(decls))
	for _, d := range decls {
		s[d.Name] = struct{}{}
	}
	return s
}
