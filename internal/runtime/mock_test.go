//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime

import (
	"context"

	"github.com/stretchr/testify/mock"
)

// MockRuntime is a testify mock for the Runtime interface.
type MockRuntime struct {
	mock.Mock
}

func (m *MockRuntime) Type() Type {
	return m.Called().Get(0).(Type)
}

func (m *MockRuntime) Available(ctx context.Context) bool {
	return m.Called(ctx).Bool(0)
}

func (m *MockRuntime) Run(ctx context.Context, cfg RunConfig) (string, error) {
	args := m.Called(ctx, cfg)
	return args.String(0), args.Error(1)
}

func (m *MockRuntime) Start(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

func (m *MockRuntime) Stop(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

func (m *MockRuntime) Remove(ctx context.Context, name string) error {
	return m.Called(ctx, name).Error(0)
}

func (m *MockRuntime) Inspect(ctx context.Context, name string) (ContainerState, error) {
	args := m.Called(ctx, name)
	cs, _ := args.Get(0).(ContainerState)
	return cs, args.Error(1)
}
