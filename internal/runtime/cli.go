package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	_execTimeout      = 30 * time.Second
	_availableTimeout = 5 * time.Second
)

// execFunc is the signature for executing a container CLI command.
type execFunc func(ctx context.Context, args ...string) (string, error)

// newExecFunc returns an execFunc that shells out to the given binary.
func newExecFunc(binary string) execFunc {
	return func(ctx context.Context, args ...string) (string, error) {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, _execTimeout)
			defer cancel()
		}
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return stderr.String(), err
		}
		return string(out), nil
	}
}

// cliRuntime provides shared container CLI operations for Docker and Podman.
type cliRuntime struct {
	binary string
	exec   execFunc
}

// containerStateJSON matches the JSON output of `inspect -f '{{json .State}}'`.
type containerStateJSON struct {
	Status string `json:"Status"`
}

func (c *cliRuntime) Run(ctx context.Context, cfg RunConfig) (string, error) {
	if cfg.Image == "" {
		return "", ErrEmptyImage
	}
	args, err := buildRunArgs(cfg)
	if err != nil {
		return "", fmt.Errorf("%s run: %w", c.binary, err)
	}
	out, err := c.exec(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("%s run: %s: %w", c.binary, strings.TrimSpace(out), err)
	}
	return strings.TrimSpace(out), nil
}

func (c *cliRuntime) Start(ctx context.Context, name string) error {
	out, err := c.exec(ctx, "start", name)
	if err != nil {
		return fmt.Errorf("%s start %s: %s: %w", c.binary, name, strings.TrimSpace(out), err)
	}
	return nil
}

func (c *cliRuntime) Stop(ctx context.Context, name string) error {
	out, err := c.exec(ctx, "stop", name)
	if err != nil {
		return fmt.Errorf("%s stop %s: %s: %w", c.binary, name, strings.TrimSpace(out), err)
	}
	return nil
}

func (c *cliRuntime) Remove(ctx context.Context, name string) error {
	out, err := c.exec(ctx, "rm", "-f", name)
	if err != nil {
		return fmt.Errorf("%s rm %s: %s: %w", c.binary, name, strings.TrimSpace(out), err)
	}
	return nil
}

func (c *cliRuntime) Inspect(ctx context.Context, name string) (ContainerState, error) {
	out, err := c.exec(ctx, "inspect", "-f", "{{json .State}}", name)
	if err != nil {
		if isNotFound(out) {
			return StateUnknown, ErrContainerNotFound
		}
		return StateUnknown, fmt.Errorf("%s inspect %s: %s: %w", c.binary, name, strings.TrimSpace(out), err)
	}
	var state containerStateJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &state); err != nil {
		return StateUnknown, fmt.Errorf("parsing %s inspect output: %w", c.binary, err)
	}
	return parseState(state.Status), nil
}

// isNotFound returns true if the CLI output indicates the container does not exist.
func isNotFound(output string) bool {
	return strings.Contains(strings.ToLower(output), "no such")
}

// parseState converts a container status string to a ContainerState.
func parseState(s string) ContainerState {
	switch strings.ToLower(s) {
	case "running":
		return StateRunning
	case "exited":
		return StateExited
	case "created":
		return StateCreated
	case "paused":
		return StatePaused
	default:
		return StateUnknown
	}
}

// buildRunArgs constructs CLI arguments from a RunConfig.
func buildRunArgs(cfg RunConfig) ([]string, error) {
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
			return nil, fmt.Errorf("port %q:%q:%q: %w", p.HostAddr, p.HostPort, p.ContPort, ErrInvalidPort)
		}
		portSpec := p.HostPort + ":" + p.ContPort
		if p.HostAddr != "" {
			portSpec = p.HostAddr + ":" + portSpec
		}
		args = append(args, "-p", portSpec)
	}
	for _, v := range cfg.Volumes {
		if v.Name == "" || v.Target == "" {
			return nil, fmt.Errorf("volume %q:%q: %w", v.Name, v.Target, ErrInvalidVolume)
		}
		args = append(args, "-v", v.Name+":"+v.Target)
	}
	args = append(args, cfg.Image)
	args = append(args, cfg.Args...)
	return args, nil
}
