// Package runtimetest provides test doubles for the runtime.Runtime interface.
package runtimetest

import (
	"context"
	"io"

	"github.com/stretchr/testify/mock"

	"github.com/ndisidore/cicada/internal/runtime"
)

// Compile-time assertion that MockRuntime implements runtime.Runtime.
var _ runtime.Runtime = (*MockRuntime)(nil)

// MockRuntime is a testify mock for the runtime.Runtime interface.
type MockRuntime struct {
	mock.Mock
}

// Type returns the runtime type.
func (m *MockRuntime) Type() runtime.Type {
	rt, _ := m.Called().Get(0).(runtime.Type) //nolint:revive // test mock; panic on misconfigured expectation is acceptable
	return rt
}

// Available reports whether the runtime binary exists and the daemon is responsive.
func (m *MockRuntime) Available(ctx context.Context) bool {
	return m.Called(ctx).Bool(0)
}

// Run creates and starts a container, returning the container ID.
func (m *MockRuntime) Run(ctx context.Context, cfg runtime.RunConfig) (string, error) {
	args := m.Called(ctx, cfg)
	return args.String(0), args.Error(1)
}

// Start starts an existing stopped container by name.
func (m *MockRuntime) Start(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// Stop stops a running container by name.
func (m *MockRuntime) Stop(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// Remove force-removes a container by name.
func (m *MockRuntime) Remove(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

// Inspect returns the current state of a container by name.
func (m *MockRuntime) Inspect(ctx context.Context, name string) (runtime.ContainerState, error) {
	args := m.Called(ctx, name)
	cs, _ := args.Get(0).(runtime.ContainerState) //nolint:revive // test mock; zero-value fallback is intentional
	return cs, args.Error(1)
}

// LoadImage loads a container image tarball from r into the runtime's local store.
// The reader is not passed to testify's Called to avoid data races between
// mock argument inspection and concurrent pipe operations.
func (m *MockRuntime) LoadImage(ctx context.Context, _ io.Reader) error {
	return m.Called(ctx).Error(0)
}
