//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/internal/runtime/runtimetest"
)

func TestResolverDetect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(tb testing.TB) *runtime.Resolver
		wantType runtime.Type
		wantErr  error
	}{
		{
			name: "first available wins",
			setup: func(tb testing.TB) *runtime.Resolver {
				tb.Helper()
				docker := new(runtimetest.MockRuntime)
				docker.On("Available", mock.Anything).Return(true).Once()
				docker.On("Type").Return(runtime.Docker).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				podman := new(runtimetest.MockRuntime)
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &runtime.Resolver{Factories: []func() runtime.Runtime{
					func() runtime.Runtime { return docker },
					func() runtime.Runtime { return podman },
				}}
			},
			wantType: runtime.Docker,
		},
		{
			name: "skips unavailable",
			setup: func(tb testing.TB) *runtime.Resolver {
				tb.Helper()
				docker := new(runtimetest.MockRuntime)
				docker.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				podman := new(runtimetest.MockRuntime)
				podman.On("Available", mock.Anything).Return(true).Once()
				podman.On("Type").Return(runtime.Podman).Once()
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &runtime.Resolver{Factories: []func() runtime.Runtime{
					func() runtime.Runtime { return docker },
					func() runtime.Runtime { return podman },
				}}
			},
			wantType: runtime.Podman,
		},
		{
			name: "none available returns ErrNoRuntime",
			setup: func(tb testing.TB) *runtime.Resolver {
				tb.Helper()
				docker := new(runtimetest.MockRuntime)
				docker.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				podman := new(runtimetest.MockRuntime)
				podman.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &runtime.Resolver{Factories: []func() runtime.Runtime{
					func() runtime.Runtime { return docker },
					func() runtime.Runtime { return podman },
				}}
			},
			wantErr: runtime.ErrNoRuntime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.setup(t)
			rt, err := r.Detect(t.Context())
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, rt.Type())
		})
	}
}

func TestResolverGet(t *testing.T) {
	t.Parallel()

	newNamed := func(tb testing.TB) *runtime.Resolver {
		tb.Helper()
		dockerMock := new(runtimetest.MockRuntime)
		dockerMock.On("Type").Return(runtime.Docker).Maybe()
		podmanMock := new(runtimetest.MockRuntime)
		podmanMock.On("Type").Return(runtime.Podman).Maybe()
		tb.Cleanup(func() {
			dockerMock.AssertExpectations(tb)
			podmanMock.AssertExpectations(tb)
		})
		return &runtime.Resolver{Named: map[runtime.Type]func() runtime.Runtime{
			runtime.Docker: func() runtime.Runtime { return dockerMock },
			runtime.Podman: func() runtime.Runtime { return podmanMock },
		}}
	}

	tests := []struct {
		name        string
		setup       func(tb testing.TB) *runtime.Resolver
		rtName      string
		wantType    runtime.Type
		wantErr     error
		wantContain string
	}{
		{
			name:     "explicit docker",
			setup:    newNamed,
			rtName:   string(runtime.Docker),
			wantType: runtime.Docker,
		},
		{
			name:     "explicit podman",
			setup:    newNamed,
			rtName:   string(runtime.Podman),
			wantType: runtime.Podman,
		},
		{
			name:    "unknown runtime",
			setup:   newNamed,
			rtName:  "containerd",
			wantErr: runtime.ErrUnknownRuntime,
		},
		{
			name:        "nil Named returns ErrUnknownRuntime",
			setup:       func(testing.TB) *runtime.Resolver { return &runtime.Resolver{} },
			rtName:      "docker",
			wantErr:     runtime.ErrUnknownRuntime,
			wantContain: "no named runtimes",
		},
		{
			name: "nil factory returns ErrNilFactory",
			setup: func(testing.TB) *runtime.Resolver {
				return &runtime.Resolver{Named: map[runtime.Type]func() runtime.Runtime{
					runtime.Docker: func() runtime.Runtime { return nil },
				}}
			},
			rtName:  "docker",
			wantErr: runtime.ErrNilFactory,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.setup(t)
			rt, err := r.Get(t.Context(), tt.rtName)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				if tt.wantContain != "" {
					assert.Contains(t, err.Error(), tt.wantContain)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, rt.Type())
		})
	}
}

func TestResolverGetAuto(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		setup    func(tb testing.TB) *runtime.Resolver
		wantType runtime.Type
		wantErr  error
	}{
		{
			name:  "auto detects first available",
			input: "auto",
			setup: func(tb testing.TB) *runtime.Resolver {
				tb.Helper()
				docker := new(runtimetest.MockRuntime)
				docker.On("Available", mock.Anything).Return(true).Once()
				docker.On("Type").Return(runtime.Docker).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				return &runtime.Resolver{Factories: []func() runtime.Runtime{
					func() runtime.Runtime { return docker },
				}}
			},
			wantType: runtime.Docker,
		},
		{
			name:  "empty string triggers auto-detect",
			input: "",
			setup: func(tb testing.TB) *runtime.Resolver {
				tb.Helper()
				podman := new(runtimetest.MockRuntime)
				podman.On("Available", mock.Anything).Return(true).Once()
				podman.On("Type").Return(runtime.Podman).Once()
				tb.Cleanup(func() { podman.AssertExpectations(tb) })

				return &runtime.Resolver{Factories: []func() runtime.Runtime{
					func() runtime.Runtime { return podman },
				}}
			},
			wantType: runtime.Podman,
		},
		{
			name:  "auto with none available",
			input: "auto",
			setup: func(tb testing.TB) *runtime.Resolver {
				tb.Helper()
				docker := new(runtimetest.MockRuntime)
				docker.On("Available", mock.Anything).Return(false).Once()
				tb.Cleanup(func() { docker.AssertExpectations(tb) })

				return &runtime.Resolver{Factories: []func() runtime.Runtime{
					func() runtime.Runtime { return docker },
				}}
			},
			wantErr: runtime.ErrNoRuntime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := tt.setup(t)
			rt, err := r.Get(t.Context(), tt.input)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, rt.Type())
		})
	}
}
