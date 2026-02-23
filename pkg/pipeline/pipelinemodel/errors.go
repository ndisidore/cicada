package pipelinemodel

import "errors"

// Sentinel errors for pipeline validation.
var (
	ErrEmptyPipeline    = errors.New("pipeline has no jobs")
	ErrEmptyJobName     = errors.New("job has empty name")
	ErrDuplicateJob     = errors.New("duplicate job name")
	ErrEmptyStepName    = errors.New("step has empty name")
	ErrDuplicateStep    = errors.New("duplicate step name")
	ErrInvalidName      = errors.New("name contains invalid characters")
	ErrMissingImage     = errors.New("job missing image")
	ErrMissingRun       = errors.New("step has no run commands")
	ErrEmptyRunCommand  = errors.New("run command is empty or whitespace-only")
	ErrEmptyJob         = errors.New("job has no steps")
	ErrSelfDependency   = errors.New("job depends on itself")
	ErrUnknownDep       = errors.New("unknown dependency")
	ErrCycleDetected    = errors.New("dependency cycle detected")
	ErrEmptyMatrix      = errors.New("matrix has no dimensions")
	ErrEmptyDimension   = errors.New("dimension has no values")
	ErrInvalidDimName   = errors.New("invalid dimension name")
	ErrDuplicateDim     = errors.New("duplicate dimension name")
	ErrMatrixTooLarge   = errors.New("matrix produces too many combinations")
	ErrInvalidCondition = errors.New("invalid when condition")
	ErrDeferredStepWhen = errors.New("step when must not reference output()")
)

// Sentinel errors for modular configuration (includes, fragments, params).
var (
	ErrCircularInclude  = errors.New("circular include detected")
	ErrIncludeDepth     = errors.New("include depth limit exceeded")
	ErrMissingParam     = errors.New("missing required parameter")
	ErrUnknownParam     = errors.New("unknown parameter")
	ErrDuplicateParam   = errors.New("duplicate parameter name")
	ErrDuplicateAlias   = errors.New("duplicate include alias")
	ErrAliasCollision   = errors.New("alias collides with job name")
	ErrInvalidConflict  = errors.New("invalid on-conflict value")
	ErrPipelineNoParams = errors.New("pipeline does not accept parameters")
)

// Sentinel errors for retry, timeout, and shell configuration.
var (
	ErrInvalidRetryAttempts = errors.New("retry attempts must be positive")
	ErrInvalidBackoff       = errors.New("invalid backoff strategy")
	ErrNegativeDelay        = errors.New("retry delay must be non-negative")
	ErrNegativeTimeout      = errors.New("timeout must be non-negative")
	ErrEmptyShell           = errors.New("shell must have at least one argument")
)

// Sentinel errors for env vars, exports, artifacts, and publish.
var (
	ErrEmptyEnvKey            = errors.New("env key is empty")
	ErrEmptyExportPath        = errors.New("export path is empty")
	ErrDuplicateExport        = errors.New("duplicate export path")
	ErrArtifactNoDep          = errors.New("artifact references job not in depends-on")
	ErrDuplicateEnvKey        = errors.New("duplicate env key")
	ErrDuplicateArtifact      = errors.New("duplicate artifact target path")
	ErrRelativeExport         = errors.New("export path must be absolute")
	ErrRelativeArtifact       = errors.New("artifact target must be absolute")
	ErrRelativeArtifactSource = errors.New("artifact source must be absolute")
	ErrEmptyArtifactFrom      = errors.New("artifact from is empty")
	ErrEmptyArtifactSource    = errors.New("artifact source is empty")
	ErrEmptyArtifactTarget    = errors.New("artifact target is empty")
	ErrRootExport             = errors.New("export path must not be root")
	ErrRootArtifact           = errors.New("artifact target must not be root")
	ErrEmptyExportLocal       = errors.New("export local path is empty")
	ErrEmptyPublishImage      = errors.New("publish image reference is empty")
)

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
	ErrSecretResolution         = errors.New("resolving secret")
	ErrSecretVarWithNonHostEnv  = errors.New("var is only valid with hostEnv source")
	ErrInvalidSecretEnvName     = errors.New("env name must match POSIX identifier syntax")
	ErrSecretPathWithNonFile    = errors.New("path is only valid with file source")
	ErrSecretCmdWithNonCmd      = errors.New("cmd is only valid with cmd source")
	ErrDuplicateSecretEnvName   = errors.New("duplicate secret env target")
	ErrDuplicateSecretMount     = errors.New("duplicate secret mount target")
	ErrInvalidSecretHostEnvName = errors.New("hostEnv lookup name must be a valid POSIX identifier (set var to override)")
)

// Sentinel errors for job filtering.
var (
	ErrUnknownStartAt       = errors.New("start-at job not found")
	ErrUnknownStopAfter     = errors.New("stop-after job not found")
	ErrUnknownStartAtStep   = errors.New("start-at step not found in job")
	ErrUnknownStopAfterStep = errors.New("stop-after step not found in job")
	ErrEmptyStepWindow      = errors.New("step window is empty (start-at step comes after stop-after step)")
	ErrEmptyFilterResult    = errors.New("filter produces no jobs")
	ErrOrphanedStep         = errors.New("step specified without job")
)

// Sentinel errors for condition evaluation.
var (
	ErrJobCondition  = errors.New("job condition not satisfiable")
	ErrStepCondition = errors.New("step condition not satisfiable")
)
