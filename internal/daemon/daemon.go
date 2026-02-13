// Package daemon manages the lifecycle of a local BuildKit daemon container.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	bkclient "github.com/moby/buildkit/client"

	"github.com/ndisidore/cicada/pkg/slogctx"

	"github.com/ndisidore/cicada/internal/runtime"
)

// ErrEngineUnhealthy indicates the engine health check timed out.
var ErrEngineUnhealthy = errors.New("engine health check timed out")

// ErrStartFailed indicates the engine container could not be started.
var ErrStartFailed = errors.New("engine start unsuccessful")

// ErrNilRuntime indicates NewManager was called with a nil runtime.
var ErrNilRuntime = errors.New("nil runtime")

const (
	_containerName = "cicada-buildkitd"
	_buildkitImage = "moby/buildkit:v0.27.1"
	_port          = "1234"
	_defaultAddr   = "tcp://127.0.0.1:" + _port
	_volumeName    = "cicada-buildkit-state"
)

// healthConfig holds timing parameters for the engine health check loop.
type healthConfig struct {
	timeout     time.Duration
	interval    time.Duration
	backoff     int
	maxInterval time.Duration
}

var _defaultHealthConfig = healthConfig{
	timeout:     30 * time.Second,
	interval:    100 * time.Millisecond,
	backoff:     2,
	maxInterval: 3 * time.Second,
}

// Manager manages the lifecycle of a local BuildKit daemon container.
type Manager struct {
	rt     runtime.Runtime
	bkDial func(ctx context.Context, addr string) (bool, error)
	health healthConfig
}

// NewManager creates a Manager backed by the given container runtime.
func NewManager(rt runtime.Runtime) (*Manager, error) {
	if rt == nil {
		return nil, ErrNilRuntime
	}
	return &Manager{
		rt:     rt,
		bkDial: defaultBkDial,
		health: _defaultHealthConfig,
	}, nil
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
// If userAddr is non-empty and differs from DefaultAddr() it is assumed
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
		slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelDebug, "buildkitd already reachable", slog.String("addr", addr))
		return addr, nil
	}

	return m.Start(ctx)
}

// IsReachable probes a buildkitd address with a short timeout.
func (m *Manager) IsReachable(ctx context.Context, addr string) bool {
	ok, _ := m.bkDial(ctx, addr)
	return ok
}

// Start runs a moby/buildkit container named "cicada-buildkitd".
// It is idempotent: if a stopped container exists, it is restarted.
// The daemon listens on TCP so no Unix socket permissions are needed.
func (m *Manager) Start(ctx context.Context) (string, error) {
	log := slogctx.FromContext(ctx).With(slog.String("runtime", string(m.rt.Type())))

	state, err := m.rt.Inspect(ctx, _containerName)
	if err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		return "", fmt.Errorf("inspecting container: %w", err)
	}

	switch state {
	case runtime.StateRunning:
		log.InfoContext(ctx, "buildkitd container already running")
	case runtime.StateExited, runtime.StateCreated:
		log.InfoContext(ctx, "restarting stopped buildkitd container")
		if err := m.rt.Start(ctx, _containerName); err != nil {
			return "", fmt.Errorf("restarting container: %w: %w", err, ErrStartFailed)
		}
	default: // includes container-not-found (zero-value state)
		log.InfoContext(ctx, "starting buildkitd container")
		if _, err := m.rt.Run(ctx, runtime.RunConfig{
			Name:       _containerName,
			Image:      _buildkitImage,
			Privileged: true,
			Detach:     true,
			Ports: []runtime.PortBinding{
				{HostAddr: "127.0.0.1", HostPort: _port, ContPort: _port},
			},
			Volumes: []runtime.VolumeMount{
				{Name: _volumeName, Target: "/var/lib/buildkit"},
			},
			Args: []string{"--addr", "tcp://0.0.0.0:" + _port},
		}); err != nil {
			return "", fmt.Errorf("creating container: %w: %w", err, ErrStartFailed)
		}
	}

	if err := m.waitHealthy(ctx); err != nil {
		return "", fmt.Errorf("starting engine: %w", err)
	}

	return _defaultAddr, nil
}

// Stop gracefully stops the cicada-buildkitd container without removing it.
// It is a no-op if the container is not running or does not exist.
func (m *Manager) Stop(ctx context.Context) error {
	state, err := m.rt.Inspect(ctx, _containerName)
	if errors.Is(err, runtime.ErrContainerNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting container: %w", err)
	}
	if state != runtime.StateRunning {
		return nil
	}

	if err := m.rt.Stop(ctx, _containerName); err != nil {
		return fmt.Errorf("stopping container: %w", err)
	}
	return nil
}

// Remove force-removes the cicada-buildkitd container (running or stopped).
func (m *Manager) Remove(ctx context.Context) error {
	_, err := m.rt.Inspect(ctx, _containerName)
	if errors.Is(err, runtime.ErrContainerNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting container: %w", err)
	}

	if err := m.rt.Remove(ctx, _containerName); err != nil {
		return fmt.Errorf("removing container: %w", err)
	}
	return nil
}

// Status returns the current container state or empty string if not found.
func (m *Manager) Status(ctx context.Context) (string, error) {
	state, err := m.rt.Inspect(ctx, _containerName)
	if errors.Is(err, runtime.ErrContainerNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspecting container: %w", err)
	}
	return string(state), nil
}

func (m *Manager) waitHealthy(ctx context.Context) error {
	deadline := time.Now().Add(m.health.timeout)
	interval := m.health.interval

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
			interval *= time.Duration(m.health.backoff)
			if interval > m.health.maxInterval {
				interval = m.health.maxInterval
			}
		}
	}
	return ErrEngineUnhealthy
}
