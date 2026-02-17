// Package docker provides a Docker container runtime adapter.
package docker

import (
	"context"

	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/internal/runtime/cliutil"
)

// Compile-time assertion that Runtime implements runtime.Runtime.
var _ runtime.Runtime = (*Runtime)(nil)

// Runtime implements runtime.Runtime using the docker CLI.
type Runtime struct {
	*cliutil.CLIRuntime
}

// New creates a Docker runtime.
func New() *Runtime {
	return &Runtime{CLIRuntime: &cliutil.CLIRuntime{
		Binary: "docker",
		Exec:   cliutil.NewExecFunc("docker"),
	}}
}

// Type returns the Docker runtime type.
func (*Runtime) Type() runtime.Type { return runtime.Docker }

// Available reports whether the docker binary exists and the daemon is responsive.
func (d *Runtime) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, cliutil.AvailableTimeout)
	defer cancel()
	_, err := d.Exec(ctx, "info", "--format", "{{json .ServerVersion}}")
	return err == nil
}
