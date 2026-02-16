//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime

import "context"

// PodmanRuntime implements Runtime using the podman CLI.
type PodmanRuntime struct {
	*cliRuntime
}

// NewPodman creates a Podman runtime.
func NewPodman() *PodmanRuntime {
	return &PodmanRuntime{cliRuntime: &cliRuntime{
		binary: "podman",
		exec:   newExecFunc("podman"),
	}}
}

// Type returns the Podman runtime type.
func (*PodmanRuntime) Type() Type { return Podman }

// Available reports whether the podman binary exists and the engine is responsive.
// On macOS, podman info fails if the Podman machine is not running.
func (p *PodmanRuntime) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, _availableTimeout)
	defer cancel()
	_, err := p.exec(ctx, "info", "--format", "{{json .Host.Os}}")
	return err == nil
}
