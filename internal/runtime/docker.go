package runtime

import "context"

// DockerRuntime implements Runtime using the docker CLI.
type DockerRuntime struct {
	*cliRuntime
}

// NewDocker creates a Docker runtime.
func NewDocker() *DockerRuntime {
	return &DockerRuntime{cliRuntime: &cliRuntime{
		binary: "docker",
		exec:   newExecFunc("docker"),
	}}
}

// Type returns the Docker runtime type.
func (*DockerRuntime) Type() Type { return Docker }

// Available reports whether the docker binary exists and the daemon is responsive.
func (d *DockerRuntime) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, _availableTimeout)
	defer cancel()
	_, err := d.exec(ctx, "info", "--format", "{{json .ServerVersion}}")
	return err == nil
}
