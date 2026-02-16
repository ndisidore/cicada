package conditional

import (
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// celBindings holds optional runtime bindings for CEL function overloads.
// A nil slice produces a declaration-only overload (used for validation).
type celBindings struct {
	env     []cel.OverloadOpt
	hostEnv []cel.OverloadOpt
	output  []cel.OverloadOpt
	matrix  []cel.OverloadOpt
}

// newCELEnv creates a CEL environment with shared variable and function
// declarations. Bindings in b, when non-nil, attach runtime behavior.
func newCELEnv(b celBindings) (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("branch", cel.StringType),
		cel.Variable("tag", cel.StringType),
		cel.Function("env",
			cel.Overload("env_string",
				[]*cel.Type{cel.StringType},
				cel.StringType,
				b.env...,
			),
		),
		cel.Function("hostEnv",
			cel.Overload("host_env_string",
				[]*cel.Type{cel.StringType},
				cel.StringType,
				b.hostEnv...,
			),
		),
		cel.Function("output",
			cel.Overload("output_string_string",
				[]*cel.Type{cel.StringType, cel.StringType},
				cel.StringType,
				b.output...,
			),
		),
		cel.Function("matrix",
			cel.Overload("matrix_string",
				[]*cel.Type{cel.StringType},
				cel.StringType,
				b.matrix...,
			),
		),
	)
}

// refToString extracts a string from a CEL ref.Val, returning "" if the
// type assertion fails. CEL's type system guarantees string inputs for our
// declared signatures, but the linter requires safe assertions.
func refToString(v ref.Val) string {
	s, ok := v.Value().(string)
	if !ok {
		return ""
	}
	return s
}

// contextBindings returns celBindings for env(), hostEnv(), and matrix()
// backed by the given Context. The output binding is left nil so callers
// can supply a static stub or a fully-resolved implementation.
func contextBindings(ctx Context) celBindings {
	return celBindings{
		env: []cel.OverloadOpt{cel.UnaryBinding(func(val ref.Val) ref.Val {
			return types.String(ctx.PipelineEnv[refToString(val)])
		})},
		hostEnv: []cel.OverloadOpt{cel.UnaryBinding(func(val ref.Val) ref.Val {
			if ctx.Getenv == nil {
				return types.String("")
			}
			return types.String(ctx.Getenv(refToString(val)))
		})},
		matrix: []cel.OverloadOpt{cel.UnaryBinding(func(val ref.Val) ref.Val {
			return types.String(ctx.Matrix[refToString(val)])
		})},
	}
}

// newValidationCELEnv creates a CEL environment for parse-time type checking.
// It declares all variable and function signatures but provides no runtime bindings.
func newValidationCELEnv() (*cel.Env, error) {
	return newCELEnv(celBindings{})
}

// newStaticCELEnv creates a CEL environment for pre-build evaluation with
// concrete bindings for branch, tag, env(), and hostEnv(). output() is bound
// to always return "" since dependency outputs are not yet available.
func newStaticCELEnv(ctx Context) (*cel.Env, error) {
	b := contextBindings(ctx)
	b.output = []cel.OverloadOpt{cel.BinaryBinding(func(_, _ ref.Val) ref.Val {
		return types.String("")
	})}
	return newCELEnv(b)
}

// newDeferredCELEnv creates a CEL environment for runtime evaluation with all
// bindings, including output() backed by actual dependency outputs.
func newDeferredCELEnv(ctx Context, depOutputs map[string]map[string]string) (*cel.Env, error) {
	b := contextBindings(ctx)
	b.output = []cel.OverloadOpt{cel.BinaryBinding(func(jobVal, keyVal ref.Val) ref.Val {
		job := refToString(jobVal)
		key := refToString(keyVal)
		if outputs, ok := depOutputs[job]; ok {
			return types.String(outputs[key])
		}
		return types.String("")
	})}
	return newCELEnv(b)
}

// astReferencesOutput walks a compiled AST to detect calls to the output() function.
func astReferencesOutput(checked *ast.AST) bool {
	if checked == nil {
		return false
	}
	var found bool
	ast.PostOrderVisit(checked.Expr(), ast.NewExprVisitor(func(e ast.Expr) {
		if found {
			return
		}
		if e.Kind() == ast.CallKind {
			if e.AsCall().FunctionName() == "output" {
				found = true
			}
		}
	}))
	return found
}
