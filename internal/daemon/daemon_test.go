package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/internal/runtime/runtimetest"
)

func noopBkDial(context.Context, string) (bool, error) { return false, nil }

func TestNewManagerNilRuntime(t *testing.T) {
	t.Parallel()
	_, err := NewManager(nil)
	require.ErrorIs(t, err, ErrNilRuntime)
}

func TestDefaultAddr(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "tcp://127.0.0.1:1234", DefaultAddr())
}

func TestEnsureRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		userAddr string
		dial     func(ctx context.Context, addr string) (bool, error)
		setup    func(m *runtimetest.MockRuntime)
		wantAddr string
		wantErr  error
	}{
		{
			name:     "custom address returned as-is",
			userAddr: "tcp://10.0.0.1:9999",
			wantAddr: "tcp://10.0.0.1:9999",
		},
		{
			name:     "already reachable",
			userAddr: "",
			dial:     func(context.Context, string) (bool, error) { return true, nil },
			wantAddr: _defaultAddr,
		},
		{
			name:     "not reachable starts daemon",
			userAddr: "",
			dial: func() func(context.Context, string) (bool, error) {
				var calls atomic.Int32
				return func(context.Context, string) (bool, error) {
					if calls.Add(1) == 1 {
						return false, nil
					}
					return true, nil
				}
			}(),
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Type").Return(runtime.Docker)
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, runtime.ErrContainerNotFound)
				m.On("Run", mock.Anything, mock.AnythingOfType("runtime.RunConfig")).Return("container-id", nil)
			},
			wantAddr: _defaultAddr,
		},
		{
			name:     "runtime run fails returns ErrStartFailed",
			userAddr: "",
			dial: func() func(context.Context, string) (bool, error) {
				var calls atomic.Int32
				return func(context.Context, string) (bool, error) {
					calls.Add(1)
					return false, nil
				}
			}(),
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Type").Return(runtime.Docker)
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, runtime.ErrContainerNotFound)
				m.On("Run", mock.Anything, mock.AnythingOfType("runtime.RunConfig")).Return("", errors.New("cannot start"))
			},
			wantErr: ErrStartFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt := new(runtimetest.MockRuntime)
			if tt.setup != nil {
				tt.setup(rt)
			}
			dial := tt.dial
			if dial == nil {
				dial = noopBkDial
			}
			mgr := &Manager{rt: rt, bkDial: dial, health: _defaultHealthConfig}

			addr, err := mgr.EnsureRunning(t.Context(), tt.userAddr)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAddr, addr)
			rt.AssertExpectations(t)
		})
	}
}

func TestIsReachable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dial func(ctx context.Context, addr string) (bool, error)
		want bool
	}{
		{
			name: "reachable",
			dial: func(context.Context, string) (bool, error) { return true, nil },
			want: true,
		},
		{
			name: "unreachable",
			dial: func(context.Context, string) (bool, error) { return false, nil },
			want: false,
		},
		{
			name: "error treated as unreachable",
			dial: func(context.Context, string) (bool, error) { return false, errors.New("boom") },
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mgr := &Manager{rt: new(runtimetest.MockRuntime), bkDial: tt.dial, health: _defaultHealthConfig}
			got := mgr.IsReachable(t.Context(), _defaultAddr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(m *runtimetest.MockRuntime)
		wantErr bool
	}{
		{
			name: "running container stops successfully",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateRunning, nil)
				m.On("Stop", mock.Anything, _containerName).Return(nil)
			},
		},
		{
			name: "stopped container is no-op",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateExited, nil)
			},
		},
		{
			name: "created container is no-op",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateCreated, nil)
			},
		},
		{
			name: "missing container is no-op",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, runtime.ErrContainerNotFound)
			},
		},
		{
			name: "runtime stop propagates error",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateRunning, nil)
				m.On("Stop", mock.Anything, _containerName).Return(errors.New("timeout"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt := new(runtimetest.MockRuntime)
			tt.setup(rt)
			mgr := &Manager{rt: rt, bkDial: noopBkDial, health: _defaultHealthConfig}

			err := mgr.Stop(t.Context())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "stopping container")
				return
			}
			require.NoError(t, err)
			rt.AssertExpectations(t)
		})
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(m *runtimetest.MockRuntime)
		wantErr bool
	}{
		{
			name: "existing container removed",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateExited, nil)
				m.On("Remove", mock.Anything, _containerName).Return(nil)
			},
		},
		{
			name: "missing container is no-op",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, runtime.ErrContainerNotFound)
			},
		},
		{
			name: "runtime rm propagates error",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateExited, nil)
				m.On("Remove", mock.Anything, _containerName).Return(errors.New("permission denied"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt := new(runtimetest.MockRuntime)
			tt.setup(rt)
			mgr := &Manager{rt: rt, bkDial: noopBkDial, health: _defaultHealthConfig}

			err := mgr.Remove(t.Context())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "removing container")
				return
			}
			require.NoError(t, err)
			rt.AssertExpectations(t)
		})
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(m *runtimetest.MockRuntime)
		wantState string
		wantErr   bool
	}{
		{
			name: "running container",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateRunning, nil)
			},
			wantState: "running",
		},
		{
			name: "exited container",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateExited, nil)
			},
			wantState: "exited",
		},
		{
			name: "missing container returns empty",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, runtime.ErrContainerNotFound)
			},
			wantState: "",
		},
		{
			name: "inspect error propagates",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, errors.New("permission denied"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt := new(runtimetest.MockRuntime)
			tt.setup(rt)
			mgr := &Manager{rt: rt, bkDial: noopBkDial, health: _defaultHealthConfig}

			state, err := mgr.Status(t.Context())
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantState, state)
			rt.AssertExpectations(t)
		})
	}
}

func TestStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(m *runtimetest.MockRuntime)
		dial    func(ctx context.Context, addr string) (bool, error)
		wantErr error
	}{
		{
			name: "creates new container when none exists",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Type").Return(runtime.Docker)
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, runtime.ErrContainerNotFound)
				m.On("Run", mock.Anything, mock.AnythingOfType("runtime.RunConfig")).Return("container-id", nil)
			},
			dial: func(context.Context, string) (bool, error) { return true, nil },
		},
		{
			name: "restarts exited container",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Type").Return(runtime.Docker)
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateExited, nil)
				m.On("Start", mock.Anything, _containerName).Return(nil)
			},
			dial: func(context.Context, string) (bool, error) { return true, nil },
		},
		{
			name: "already running skips create",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Type").Return(runtime.Docker)
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateRunning, nil)
			},
			dial: func(context.Context, string) (bool, error) { return true, nil },
		},
		{
			name: "run failure returns ErrStartFailed",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Type").Return(runtime.Docker)
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateUnknown, runtime.ErrContainerNotFound)
				m.On("Run", mock.Anything, mock.AnythingOfType("runtime.RunConfig")).Return("", errors.New("image not found"))
			},
			wantErr: ErrStartFailed,
		},
		{
			name: "restart failure returns ErrStartFailed",
			setup: func(m *runtimetest.MockRuntime) {
				m.On("Type").Return(runtime.Docker)
				m.On("Inspect", mock.Anything, _containerName).Return(runtime.StateExited, nil)
				m.On("Start", mock.Anything, _containerName).Return(errors.New("cannot start"))
			},
			wantErr: ErrStartFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt := new(runtimetest.MockRuntime)
			tt.setup(rt)
			dial := tt.dial
			if dial == nil {
				dial = noopBkDial
			}
			mgr := &Manager{rt: rt, bkDial: dial, health: _defaultHealthConfig}

			addr, err := mgr.Start(t.Context())
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, _defaultAddr, addr)
			rt.AssertExpectations(t)
		})
	}
}

func TestWaitHealthy(t *testing.T) {
	t.Parallel()

	health := healthConfig{
		timeout:     10 * time.Second,
		interval:    1 * time.Second,
		backoff:     2,
		maxInterval: 4 * time.Second,
	}

	t.Run("succeeds on first probe", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			mgr := &Manager{
				rt:     new(runtimetest.MockRuntime),
				bkDial: func(context.Context, string) (bool, error) { return true, nil },
				health: health,
			}
			require.NoError(t, mgr.waitHealthy(t.Context()))
		})
	})

	t.Run("succeeds after retries with backoff", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			mgr := &Manager{
				rt: new(runtimetest.MockRuntime),
				bkDial: func(context.Context, string) (bool, error) {
					// Succeed on the 3rd attempt.
					return calls.Add(1) >= 3, nil
				},
				health: health,
			}
			require.NoError(t, mgr.waitHealthy(t.Context()))
			assert.Equal(t, int32(3), calls.Load())
		})
	})

	t.Run("returns ErrEngineUnhealthy on timeout", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			mgr := &Manager{
				rt:     new(runtimetest.MockRuntime),
				bkDial: func(context.Context, string) (bool, error) { return false, nil },
				health: health,
			}
			require.ErrorIs(t, mgr.waitHealthy(t.Context()), ErrEngineUnhealthy)
		})
	})

	t.Run("returns error on dial failure", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			mgr := &Manager{
				rt:     new(runtimetest.MockRuntime),
				bkDial: func(context.Context, string) (bool, error) { return false, errors.New("connection refused") },
				health: health,
			}
			err := mgr.waitHealthy(t.Context())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "probing buildkitd")
		})
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		t.Parallel()
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var calls atomic.Int32
			mgr := &Manager{
				rt: new(runtimetest.MockRuntime),
				bkDial: func(context.Context, string) (bool, error) {
					if calls.Add(1) == 2 {
						cancel()
					}
					return false, nil
				},
				health: health,
			}
			err := mgr.waitHealthy(ctx)
			require.ErrorIs(t, err, context.Canceled)
		})
	})
}
