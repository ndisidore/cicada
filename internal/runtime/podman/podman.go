// Package podman provides a Podman container runtime adapter.
package podman

import (
	"context"

	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/internal/runtime/cliutil"
)

// Compile-time assertion that Runtime implements runtime.Runtime.
var _ runtime.Runtime = (*Runtime)(nil)

// Runtime implements runtime.Runtime using the podman CLI.
type Runtime struct {
	*cliutil.CLIRuntime
}

// New creates a Podman runtime.
func New() *Runtime {
	return &Runtime{CLIRuntime: &cliutil.CLIRuntime{
		Binary: "podman",
		Exec:   cliutil.NewExecFunc("podman"),
	}}
}

// Type returns the Podman runtime type.
func (*Runtime) Type() runtime.Type { return runtime.Podman }

// Available reports whether the podman binary exists and the engine is responsive.
// On macOS, podman info fails if the Podman machine is not running.
func (p *Runtime) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, cliutil.AvailableTimeout)
	defer cancel()
	_, err := p.Exec(ctx, "info", "--format", "{{json .Host.Os}}")
	return err == nil
}
