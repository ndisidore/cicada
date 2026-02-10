package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopDockerExec(context.Context, ...string) (string, error) { return "", nil }
func noopBkDial(context.Context, string) (bool, error)          { return false, nil }
func noopLookPath(string) (string, error)                       { return "/usr/bin/docker", nil }

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
		docker   func(ctx context.Context, args ...string) (string, error)
		lp       func(file string) (string, error)
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
					// First call (from EnsureRunning): unreachable.
					// Subsequent calls (from waitHealthy): reachable.
					if calls.Add(1) == 1 {
						return false, nil
					}
					return true, nil
				}
			}(),
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "error: no such object: cicada-buildkitd", errors.New("exit 1")
				}
				return "container-id\n", nil
			},
			wantAddr: _defaultAddr,
		},
		{
			name:     "docker not found returns ErrDockerNotFound",
			userAddr: "",
			dial:     func(context.Context, string) (bool, error) { return false, nil },
			lp:       func(string) (string, error) { return "", errors.New("not found") },
			wantErr:  ErrDockerNotFound,
		},
		{
			name:     "docker run fails returns ErrStartFailed",
			userAddr: "",
			dial: func() func(context.Context, string) (bool, error) {
				var calls atomic.Int32
				return func(context.Context, string) (bool, error) {
					calls.Add(1)
					return false, nil
				}
			}(),
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "error: no such object: cicada-buildkitd", errors.New("exit 1")
				}
				return "cannot start\n", errors.New("exit 1")
			},
			wantErr: ErrStartFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lp := tt.lp
			if lp == nil {
				lp = noopLookPath
			}
			mgr := &Manager{
				dockerExec: tt.docker,
				bkDial:     tt.dial,
				lookPath:   lp,
			}
			if mgr.dockerExec == nil {
				mgr.dockerExec = noopDockerExec
			}
			if mgr.bkDial == nil {
				mgr.bkDial = noopBkDial
			}

			addr, err := mgr.EnsureRunning(context.Background(), tt.userAddr)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAddr, addr)
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

			mgr := &Manager{
				dockerExec: noopDockerExec,
				bkDial:     tt.dial,
				lookPath:   noopLookPath,
			}
			got := mgr.IsReachable(context.Background(), _defaultAddr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		docker  func(ctx context.Context, args ...string) (string, error)
		lp      func(file string) (string, error)
		wantErr error
	}{
		{
			name: "running container stops successfully",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "running\n", nil
				}
				return "", nil
			},
		},
		{
			name: "stopped container is no-op",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "exited\n", nil
				}
				return "", nil
			},
		},
		{
			name: "created container is no-op",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "created\n", nil
				}
				return "", nil
			},
		},
		{
			name: "missing container is no-op",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "Error: No such container: cicada-buildkitd", errors.New("exit 1")
				}
				return "", nil
			},
		},
		{
			name:    "docker not found",
			lp:      func(string) (string, error) { return "", errors.New("not found") },
			wantErr: ErrDockerNotFound,
		},
		{
			name: "docker stop fails propagates error",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "running\n", nil
				}
				return "timeout\n", errors.New("exit 1")
			},
			wantErr: errors.New("stopping container"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lp := tt.lp
			if lp == nil {
				lp = noopLookPath
			}
			docker := tt.docker
			if docker == nil {
				docker = noopDockerExec
			}
			mgr := &Manager{
				dockerExec: docker,
				bkDial:     noopBkDial,
				lookPath:   lp,
			}
			err := mgr.Stop(context.Background())
			if tt.wantErr != nil {
				require.Error(t, err)
				if errors.Is(tt.wantErr, ErrDockerNotFound) {
					assert.ErrorIs(t, err, ErrDockerNotFound)
				} else {
					assert.Contains(t, err.Error(), tt.wantErr.Error())
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		docker  func(ctx context.Context, args ...string) (string, error)
		lp      func(file string) (string, error)
		wantErr error
	}{
		{
			name: "existing container removed",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "exited\n", nil
				}
				return "", nil
			},
		},
		{
			name: "missing container is no-op",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "Error: No such container: cicada-buildkitd", errors.New("exit 1")
				}
				return "", nil
			},
		},
		{
			name:    "docker not found",
			lp:      func(string) (string, error) { return "", errors.New("not found") },
			wantErr: ErrDockerNotFound,
		},
		{
			name: "docker rm fails propagates error",
			docker: func(_ context.Context, args ...string) (string, error) {
				if len(args) > 0 && args[0] == "inspect" {
					return "exited\n", nil
				}
				return "permission denied\n", errors.New("exit 1")
			},
			wantErr: errors.New("removing container"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lp := tt.lp
			if lp == nil {
				lp = noopLookPath
			}
			docker := tt.docker
			if docker == nil {
				docker = noopDockerExec
			}
			mgr := &Manager{
				dockerExec: docker,
				bkDial:     noopBkDial,
				lookPath:   lp,
			}
			err := mgr.Remove(context.Background())
			if tt.wantErr != nil {
				require.Error(t, err)
				if errors.Is(tt.wantErr, ErrDockerNotFound) {
					assert.ErrorIs(t, err, ErrDockerNotFound)
				} else {
					assert.Contains(t, err.Error(), tt.wantErr.Error())
				}
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		docker    func(ctx context.Context, args ...string) (string, error)
		lp        func(file string) (string, error)
		wantState string
		wantErr   error
	}{
		{
			name: "running container",
			docker: func(context.Context, ...string) (string, error) {
				return "running\n", nil
			},
			wantState: "running",
		},
		{
			name: "exited container",
			docker: func(context.Context, ...string) (string, error) {
				return "exited\n", nil
			},
			wantState: "exited",
		},
		{
			name: "missing container returns empty",
			docker: func(context.Context, ...string) (string, error) {
				return "Error: No such container: cicada-buildkitd", errors.New("exit 1")
			},
			wantState: "",
		},
		{
			name:    "docker not found",
			lp:      func(string) (string, error) { return "", errors.New("not found") },
			wantErr: ErrDockerNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lp := tt.lp
			if lp == nil {
				lp = noopLookPath
			}
			docker := tt.docker
			if docker == nil {
				docker = noopDockerExec
			}
			mgr := &Manager{
				dockerExec: docker,
				bkDial:     noopBkDial,
				lookPath:   lp,
			}
			state, err := mgr.Status(context.Background())
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantState, state)
		})
	}
}

func TestContainerState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		docker    func(ctx context.Context, args ...string) (string, error)
		wantState string
		wantErr   bool
	}{
		{
			name: "running container",
			docker: func(context.Context, ...string) (string, error) {
				return "running\n", nil
			},
			wantState: "running",
		},
		{
			name: "no such container",
			docker: func(context.Context, ...string) (string, error) {
				return "Error: No such container: cicada-buildkitd", errors.New("exit 1")
			},
			wantState: "",
		},
		{
			name: "no such object",
			docker: func(context.Context, ...string) (string, error) {
				return "Error: No such object: cicada-buildkitd", errors.New("exit 1")
			},
			wantState: "",
		},
		{
			name: "unexpected docker error",
			docker: func(context.Context, ...string) (string, error) {
				return "permission denied", errors.New("exit 1")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mgr := &Manager{
				dockerExec: tt.docker,
				bkDial:     noopBkDial,
				lookPath:   noopLookPath,
			}
			state, err := mgr.containerState(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantState, state)
		})
	}
}
