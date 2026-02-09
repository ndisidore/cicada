// Package daemon manages the lifecycle of a local BuildKit daemon container.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	bkclient "github.com/moby/buildkit/client"
)

// ErrDockerNotFound indicates the docker binary is not in PATH.
var ErrDockerNotFound = errors.New("docker binary not found in PATH")

// ErrEngineUnhealthy indicates the engine health check timed out.
var ErrEngineUnhealthy = errors.New("engine health check timed out")

// ErrStartFailed indicates the engine container could not be started.
var ErrStartFailed = errors.New("engine start unsuccessful")

const (
	_containerName = "ciro-buildkitd"
	_buildkitImage = "moby/buildkit:v0.27.1"
	_port          = "1234"
	_defaultAddr   = "tcp://127.0.0.1:" + _port
	_volumeName    = "ciro-buildkit-state"

	_healthTimeout     = 30 * time.Second
	_healthInterval    = 100 * time.Millisecond
	_healthBackoff     = 2
	_healthMaxInterval = 3 * time.Second
	_dockerTimeout     = 30 * time.Second
)

// Manager manages the lifecycle of a local BuildKit daemon container.
// The dockerExec, bkDial, and lookPath fields are injected for testability.
type Manager struct {
	dockerExec func(ctx context.Context, args ...string) (string, error)
	bkDial     func(ctx context.Context, addr string) (bool, error)
	lookPath   func(file string) (string, error)
}

// NewManager creates a Manager with production docker/buildkit implementations.
func NewManager() *Manager {
	return &Manager{
		dockerExec: defaultDockerExec,
		bkDial:     defaultBkDial,
		lookPath:   exec.LookPath,
	}
}

func defaultDockerExec(ctx context.Context, args ...string) (string, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, _dockerTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func defaultBkDial(ctx context.Context, addr string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	c, err := bkclient.New(probeCtx, addr)
	if err != nil {
		return false, nil //nolint:nilerr // connection failure means unreachable
	}
	defer func() { _ = c.Close() }()

	// The constructor is lazy; force a round-trip to verify connectivity.
	_, err = c.ListWorkers(probeCtx)
	return err == nil, nil
}

// DefaultAddr returns the default daemon address.
func DefaultAddr() string { return _defaultAddr }

// EnsureRunning checks if buildkitd is reachable at addr.
// If userAddr is non-empty and differs from DefaultAddr(), it is assumed
// to be user-managed and returned as-is without starting a daemon.
// Otherwise, it starts a local daemon container if needed.
func (m *Manager) EnsureRunning(ctx context.Context, userAddr string) (string, error) {
	if userAddr != "" && userAddr != _defaultAddr {
		return userAddr, nil
	}

	addr := _defaultAddr
	ok, err := m.bkDial(ctx, addr)
	if err != nil {
		return "", fmt.Errorf("probing buildkitd: %w", err)
	}
	if ok {
		slog.Default().DebugContext(ctx, "buildkitd already reachable", slog.String("addr", addr))
		return addr, nil
	}

	return m.Start(ctx)
}

// IsReachable probes a buildkitd address with a short timeout.
func (m *Manager) IsReachable(ctx context.Context, addr string) bool {
	ok, _ := m.bkDial(ctx, addr)
	return ok
}

// Start runs a moby/buildkit container named "ciro-buildkitd".
// It is idempotent: if a stopped container exists, it is restarted.
// The daemon listens on TCP so no Unix socket permissions are needed.
func (m *Manager) Start(ctx context.Context) (string, error) {
	if _, err := m.lookPath("docker"); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDockerNotFound, err)
	}

	log := slog.Default()

	// Check if container already exists (running or stopped).
	state, err := m.containerState(ctx)
	if err != nil {
		return "", fmt.Errorf("inspecting container: %w", err)
	}

	switch state {
	case "running":
		log.InfoContext(ctx, "buildkitd container already running")
	case "exited", "created":
		log.InfoContext(ctx, "restarting stopped buildkitd container")
		if out, err := m.dockerExec(ctx, "start", _containerName); err != nil {
			return "", fmt.Errorf("restarting container: %s: %w: %w", strings.TrimSpace(out), err, ErrStartFailed)
		}
	default:
		// Container doesn't exist; create it.
		log.InfoContext(ctx, "starting buildkitd container")
		args := []string{
			"run", "-d",
			"--name", _containerName,
			"--privileged", // required for BuildKit to perform nested container operations
			"-p", "127.0.0.1:" + _port + ":" + _port,
			"-v", _volumeName + ":/var/lib/buildkit",
			_buildkitImage,
			"--addr", "tcp://0.0.0.0:" + _port,
		}
		if out, err := m.dockerExec(ctx, args...); err != nil {
			return "", fmt.Errorf("creating container: %s: %w: %w", strings.TrimSpace(out), err, ErrStartFailed)
		}
	}

	if err := m.waitHealthy(ctx); err != nil {
		return "", fmt.Errorf("starting engine: %w", err)
	}

	return _defaultAddr, nil
}

// Stop gracefully stops the ciro-buildkitd container without removing it.
// It is a no-op if the container is not running.
func (m *Manager) Stop(ctx context.Context) error {
	if _, err := m.lookPath("docker"); err != nil {
		return fmt.Errorf("%w: %w", ErrDockerNotFound, err)
	}

	state, err := m.containerState(ctx)
	if err != nil {
		return fmt.Errorf("inspecting container: %w", err)
	}
	if state == "" || state == "exited" {
		return nil
	}

	if _, err := m.dockerExec(ctx, "stop", _containerName); err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}
	return nil
}

// Remove force-removes the ciro-buildkitd container (running or stopped).
func (m *Manager) Remove(ctx context.Context) error {
	if _, err := m.lookPath("docker"); err != nil {
		return fmt.Errorf("%w: %w", ErrDockerNotFound, err)
	}

	state, err := m.containerState(ctx)
	if err != nil {
		return fmt.Errorf("inspecting container: %w", err)
	}
	if state == "" {
		return nil
	}

	if _, err := m.dockerExec(ctx, "rm", "-f", _containerName); err != nil {
		return fmt.Errorf("removing container: %w", err)
	}
	return nil
}

// Status returns the current container state or empty string if not found.
func (m *Manager) Status(ctx context.Context) (string, error) {
	if _, err := m.lookPath("docker"); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDockerNotFound, err)
	}
	return m.containerState(ctx)
}

func (m *Manager) containerState(ctx context.Context) (string, error) {
	out, err := m.dockerExec(ctx, "inspect", "-f", "{{.State.Status}}", _containerName)
	if err != nil {
		// Only swallow "not found" errors; propagate unexpected failures.
		lower := strings.ToLower(out)
		if strings.Contains(lower, "no such object") || strings.Contains(lower, "no such container") {
			return "", nil
		}
		return "", fmt.Errorf("docker inspect: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (m *Manager) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(_healthTimeout)
	interval := _healthInterval

	for time.Now().Before(deadline) {
		ok, err := m.bkDial(ctx, _defaultAddr)
		if err != nil {
			return fmt.Errorf("probing buildkitd: %w", err)
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for healthy engine: %w", ctx.Err())
		case <-time.After(interval):
			interval *= _healthBackoff
			if interval > _healthMaxInterval {
				interval = _healthMaxInterval
			}
		}
	}
	return ErrEngineUnhealthy
}
