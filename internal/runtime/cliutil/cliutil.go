// Package cliutil provides shared infrastructure for CLI-based container
// runtime adapters (Docker, Podman, etc.).
package cliutil

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ndisidore/cicada/internal/runtime"
)

const (
	// _execTimeout is the default timeout for container CLI commands.
	_execTimeout = 30 * time.Second
	// _gracePeriod is how long to wait after SIGINT before SIGKILL.
	_gracePeriod = 3 * time.Second
	// AvailableTimeout is the timeout for runtime availability probes.
	AvailableTimeout = 5 * time.Second
)

var (
	errExecTimeout = errors.New("container CLI command timed out")
	errLoadTimeout = errors.New("image load timed out")
)

// interruptOrNoop returns a Cancel func that sends SIGINT if the process
// has started, or no-ops if the context was cancelled before exec.
func interruptOrNoop(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(os.Interrupt)
		}
		return nil
	}
}

// prepareCmd builds a configured *exec.Cmd with a default timeout (if none is
// set on ctx), graceful SIGINT cancellation, and a stderr capture buffer.
func prepareCmd(ctx context.Context, timeoutCause error, binary string, args ...string) (cmd *exec.Cmd, stderr *bytes.Buffer, cancel context.CancelFunc) {
	cancel = func() {} // no-op default
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeoutCause(ctx, _execTimeout, timeoutCause)
	}
	stderr = new(bytes.Buffer)
	cmd = exec.CommandContext(ctx, binary, args...) //nolint:gosec // binary is set by trusted adapter constructors (docker/podman), not user input.
	cmd.Cancel = interruptOrNoop(cmd)
	cmd.WaitDelay = _gracePeriod
	cmd.Stderr = stderr
	return cmd, stderr, cancel
}

// ExecFunc is the signature for executing a container CLI command.
type ExecFunc func(ctx context.Context, args ...string) (string, error)

// NewExecFunc returns an ExecFunc that shells out to the given binary.
// On context cancellation, SIGINT is sent first; SIGKILL follows after a grace period.
func NewExecFunc(binary string) ExecFunc {
	return func(ctx context.Context, args ...string) (string, error) {
		cmd, stderr, cancel := prepareCmd(ctx, errExecTimeout, binary, args...)
		defer cancel()
		out, err := cmd.Output()
		if err != nil {
			return stderr.String(), err
		}
		return string(out), nil
	}
}

// CLIRuntime provides shared container CLI operations for Docker and Podman.
type CLIRuntime struct {
	Binary string
	Exec   ExecFunc
}

// containerStateJSON matches the JSON output of `inspect -f '{{json .State}}'`.
type containerStateJSON struct {
	Status string `json:"Status"`
}

// Run creates and starts a container, returning the container ID.
func (c *CLIRuntime) Run(ctx context.Context, cfg runtime.RunConfig) (string, error) {
	if cfg.Image == "" {
		return "", runtime.ErrEmptyImage
	}
	args, err := BuildRunArgs(cfg)
	if err != nil {
		return "", fmt.Errorf("%s run: %w", c.Binary, err)
	}
	out, err := c.Exec(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("%s run: %s: %w", c.Binary, strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out), nil
}

// Start starts an existing stopped container by name.
func (c *CLIRuntime) Start(ctx context.Context, name string) error {
	out, err := c.Exec(ctx, "start", name)
	if err != nil {
		return fmt.Errorf("%s start %s: %s: %w", c.Binary, name, strings.TrimSpace(out), err)
	}
	return nil
}

// Stop stops a running container by name.
func (c *CLIRuntime) Stop(ctx context.Context, name string) error {
	out, err := c.Exec(ctx, "stop", name)
	if err != nil {
		return fmt.Errorf("%s stop %s: %s: %w", c.Binary, name, strings.TrimSpace(out), err)
	}
	return nil
}

// Remove force-removes a container by name.
func (c *CLIRuntime) Remove(ctx context.Context, name string) error {
	out, err := c.Exec(ctx, "rm", "-f", name)
	if err != nil {
		return fmt.Errorf("%s rm %s: %s: %w", c.Binary, name, strings.TrimSpace(out), err)
	}
	return nil
}

// Inspect returns the current state of a container by name.
func (c *CLIRuntime) Inspect(ctx context.Context, name string) (runtime.ContainerState, error) {
	out, err := c.Exec(ctx, "inspect", "-f", "{{json .State}}", name)
	if err != nil {
		if IsNotFound(out) {
			return runtime.StateUnknown, runtime.ErrContainerNotFound
		}
		return runtime.StateUnknown, fmt.Errorf("%s inspect %s: %s: %w", c.Binary, name, strings.TrimSpace(out), err)
	}
	var state containerStateJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &state); err != nil {
		return runtime.StateUnknown, fmt.Errorf("parsing %s inspect output: %w", c.Binary, err)
	}
	return ParseState(state.Status), nil
}

// LoadImage pipes an image tarball from r into `<binary> load`.
// On context cancellation, SIGINT is sent first; SIGKILL follows after a grace period.
func (c *CLIRuntime) LoadImage(ctx context.Context, r io.Reader) error {
	cmd, stderr, cancel := prepareCmd(ctx, errLoadTimeout, c.Binary, "load")
	defer cancel()
	cmd.Stdin = r
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%s load: %s: %w", c.Binary, stderr.String(), err)
		}
		return fmt.Errorf("%s load: %w", c.Binary, err)
	}
	return nil
}

// IsNotFound returns true if the CLI output indicates the container does not exist.
func IsNotFound(output string) bool {
	return strings.Contains(strings.ToLower(output), "no such")
}

// ParseState converts a container status string to a ContainerState.
func ParseState(s string) runtime.ContainerState {
	switch strings.ToLower(s) {
	case "running":
		return runtime.StateRunning
	case "exited":
		return runtime.StateExited
	case "created":
		return runtime.StateCreated
	case "paused":
		return runtime.StatePaused
	default:
		return runtime.StateUnknown
	}
}

// BuildRunArgs constructs CLI arguments from a RunConfig.
func BuildRunArgs(cfg runtime.RunConfig) ([]string, error) {
	args := make([]string, 1, 8+2*len(cfg.Ports)+2*len(cfg.Volumes)+len(cfg.Args))
	args[0] = "run"
	if cfg.Detach {
		args = append(args, "-d")
	}
	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}
	if cfg.Privileged {
		args = append(args, "--privileged")
	}
	for _, p := range cfg.Ports {
		if p.HostPort == "" || p.ContPort == "" {
			return nil, fmt.Errorf("port %q:%q:%q: %w", p.HostAddr, p.HostPort, p.ContPort, runtime.ErrInvalidPort)
		}
		portSpec := p.HostPort + ":" + p.ContPort
		if p.HostAddr != "" {
			portSpec = p.HostAddr + ":" + portSpec
		}
		args = append(args, "-p", portSpec)
	}
	for _, v := range cfg.Volumes {
		if v.Name == "" || v.Target == "" {
			return nil, fmt.Errorf("volume %q:%q: %w", v.Name, v.Target, runtime.ErrInvalidVolume)
		}
		args = append(args, "-v", v.Name+":"+v.Target)
	}
	args = append(args, cfg.Image)
	args = append(args, cfg.Args...)
	return args, nil
}
