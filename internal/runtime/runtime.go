// Package runtime abstracts container runtime operations (Docker, Podman, etc.)
// for managing the BuildKit daemon container lifecycle.
package runtime

import (
	"context"
	"errors"
)

// ContainerState represents the state of a container.
type ContainerState string

// Well-known container states returned by Runtime.Inspect.
const (
	StateRunning ContainerState = "running"
	StateExited  ContainerState = "exited"
	StateCreated ContainerState = "created"
	StatePaused  ContainerState = "paused"
	StateUnknown ContainerState = ""
)

// Type identifies a container runtime.
type Type string

// Well-known container runtime types.
const (
	Docker Type = "docker"
	Podman Type = "podman"
)

var (
	// ErrRuntimeNotFound indicates the requested runtime binary is not available.
	ErrRuntimeNotFound = errors.New("container runtime not found")
	// ErrNoRuntime indicates no container runtime could be detected.
	ErrNoRuntime = errors.New("no container runtime available")
	// ErrContainerNotFound indicates the inspected container does not exist.
	ErrContainerNotFound = errors.New("container not found")
	// ErrEmptyImage indicates a RunConfig was provided without an image.
	ErrEmptyImage = errors.New("empty image in run config")
	// ErrInvalidPort indicates a port binding is missing required fields.
	ErrInvalidPort = errors.New("invalid port binding")
	// ErrInvalidVolume indicates a volume mount is missing required fields.
	ErrInvalidVolume = errors.New("invalid volume mount")
	// ErrUnknownRuntime indicates the requested runtime name is not recognized.
	ErrUnknownRuntime = errors.New("unknown runtime")
)

// PortBinding maps a host address:port to a container port.
type PortBinding struct {
	HostAddr string
	HostPort string
	ContPort string
}

// VolumeMount maps a named volume to a container path.
type VolumeMount struct {
	Name   string
	Target string
}

// RunConfig describes a container to create and start.
type RunConfig struct {
	Name       string
	Image      string
	Privileged bool
	Ports      []PortBinding
	Volumes    []VolumeMount
	Detach     bool
	Args       []string
}

// Runtime abstracts container runtime operations for managing containers.
type Runtime interface {
	// Type returns the runtime type (e.g. Docker, Podman).
	Type() Type
	// Available reports whether the runtime binary exists and the daemon is responsive.
	Available(ctx context.Context) bool
	// Run creates and starts a container, returning the container ID.
	Run(ctx context.Context, cfg RunConfig) (string, error)
	// Start starts an existing stopped container by name.
	Start(ctx context.Context, name string) error
	// Stop stops a running container by name.
	Stop(ctx context.Context, name string) error
	// Remove force-removes a container by name.
	Remove(ctx context.Context, name string) error
	// Inspect returns the current state of a container by name.
	// Returns ErrContainerNotFound if the container does not exist.
	Inspect(ctx context.Context, name string) (ContainerState, error)
}
