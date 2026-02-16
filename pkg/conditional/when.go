// Package conditional provides CEL-based condition evaluation for pipeline
// when expressions. It handles parse-time validation, static (pre-build)
// evaluation, and deferred (post-dependency) evaluation of conditions.
package conditional

import (
	"errors"
	"fmt"

	"github.com/google/cel-go/cel"
)

// Sentinel errors for when condition evaluation.
var (
	ErrInvalidWhenExpr     = errors.New("invalid when expression")
	ErrWhenNotBool         = errors.New("when expression must return bool")
	ErrDeferredInStep      = errors.New("output() is not allowed in step-level when")
	ErrDeferredEvaluate    = errors.New("cannot evaluate deferred expression with static context")
	ErrNotDeferredEvaluate = errors.New("cannot evaluate non-deferred expression with deferred context")
)

// When represents a conditional execution expression.
type When struct {
	Expression string
	Deferred   bool     // true if expression references output(); set during construction
	parsed     *cel.Ast // cached parse tree; reused across Check calls to skip re-parsing
}

// Context provides the runtime values available for CEL evaluation.
type Context struct {
	Getenv      func(string) string
	Branch      string
	Tag         string
	PipelineEnv map[string]string // pipeline-declared env vars scoped to current evaluation context
	Matrix      map[string]string // matrix dimension values for the current expanded variant; nil for non-matrix jobs
}

// parseAndValidate parses an expression and type-checks it against the
// validation CEL environment. It returns the parsed (pre-check) AST for
// reuse in evaluation, the deferred flag, and any validation error.
func parseAndValidate(expr string) (parsed *cel.Ast, deferred bool, err error) {
	env, err := newValidationCELEnv()
	if err != nil {
		return nil, false, fmt.Errorf("creating validation env: %w", err)
	}
	parsed, issues := env.Parse(expr)
	if issues != nil && issues.Err() != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrInvalidWhenExpr, issues.Err())
	}
	checked, issues := env.Check(parsed)
	if issues != nil && issues.Err() != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrInvalidWhenExpr, issues.Err())
	}
	if checked.OutputType() != cel.BoolType {
		return nil, false, fmt.Errorf(
			"%w: got %s", ErrWhenNotBool, checked.OutputType(),
		)
	}
	return parsed, astReferencesOutput(checked.NativeRep()), nil
}

// NewWhen parses, validates, and caches the parsed AST for the given
// expression. Prefer this over constructing When literals to enable
// single-parse evaluation.
func NewWhen(expr string) (*When, error) {
	parsed, deferred, err := parseAndValidate(expr)
	if err != nil {
		return nil, err
	}
	return &When{Expression: expr, Deferred: deferred, parsed: parsed}, nil
}

// ValidateWhenExpr compiles an expression against the CEL environment and
// checks it returns bool. Returns deferred=true if the expression references
// output(), indicating it needs runtime (post-dependency) evaluation.
func ValidateWhenExpr(expr string) (deferred bool, err error) {
	_, deferred, err = parseAndValidate(expr)
	return deferred, err
}

// InvalidateAST clears the cached parse tree so the expression will be
// re-parsed on the next Evaluate/EvaluateDeferred call. Call this after
// mutating Expression externally (e.g. parameter substitution).
//
// Not safe for concurrent use. Callers must ensure single-goroutine
// ownership during substitution; concurrent evaluation requires distinct
// When values (e.g. via Clone before Expand).
func (w *When) InvalidateAST() {
	w.parsed = nil
}

// Validate compiles the expression against the CEL environment and checks
// it returns bool. It does not evaluate the expression.
func (w *When) Validate() error {
	parsed, deferred, err := parseAndValidate(w.Expression)
	if err != nil {
		return err
	}
	w.parsed = parsed
	w.Deferred = deferred
	return nil
}

// Evaluate evaluates a static (non-deferred) When expression against the
// provided context. Returns true if the job/step should execute.
func (w *When) Evaluate(ctx Context) (bool, error) {
	if w.Deferred {
		return false, ErrDeferredEvaluate
	}
	env, err := newStaticCELEnv(ctx)
	if err != nil {
		return false, fmt.Errorf("creating static CEL env: %w", err)
	}
	return evalExpr(env, w.Expression, ctx, w.parsed)
}

// EvaluateDeferred evaluates a deferred When expression with full context
// including dependency outputs.
func (w *When) EvaluateDeferred(ctx Context, depOutputs map[string]map[string]string) (bool, error) {
	if !w.Deferred {
		return false, ErrNotDeferredEvaluate
	}
	env, err := newDeferredCELEnv(ctx, depOutputs)
	if err != nil {
		return false, fmt.Errorf("creating deferred CEL env: %w", err)
	}
	return evalExpr(env, w.Expression, ctx, w.parsed)
}

// evalExpr type-checks and evaluates a CEL expression. If parsed is non-nil,
// only the Check phase runs (skipping re-parse); otherwise a full Compile
// is performed as a fallback.
func evalExpr(env *cel.Env, expr string, ctx Context, parsed *cel.Ast) (bool, error) {
	var compiled *cel.Ast
	if parsed != nil {
		var issues *cel.Issues
		compiled, issues = env.Check(parsed)
		if issues != nil && issues.Err() != nil {
			return false, fmt.Errorf("%w: %w", ErrInvalidWhenExpr, issues.Err())
		}
	} else {
		var issues *cel.Issues
		compiled, issues = env.Compile(expr)
		if issues != nil && issues.Err() != nil {
			return false, fmt.Errorf("%w: %w", ErrInvalidWhenExpr, issues.Err())
		}
	}
	prg, err := env.Program(compiled)
	if err != nil {
		return false, fmt.Errorf("creating program: %w", err)
	}
	out, _, err := prg.Eval(map[string]any{
		"branch": ctx.Branch,
		"tag":    ctx.Tag,
	})
	if err != nil {
		return false, fmt.Errorf("evaluating expression %q: %w", expr, err)
	}
	result, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("%w: got %T", ErrWhenNotBool, out.Value())
	}
	return result, nil
}
