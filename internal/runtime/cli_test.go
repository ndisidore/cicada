//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIRuntimeRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cfg        RunConfig
		exec       execFunc
		want       string
		wantErr    error
		wantAnyErr bool
	}{
		{
			name: "simple run returns trimmed container ID",
			cfg: RunConfig{
				Name:  "test-container",
				Image: "alpine:latest",
			},
			exec: func(_ context.Context, args ...string) (string, error) {
				return "abc123\n", nil
			},
			want: "abc123",
		},
		{
			name:    "empty image returns ErrEmptyImage",
			cfg:     RunConfig{Name: "test-container"},
			wantErr: ErrEmptyImage,
		},
		{
			name: "exec error wraps with binary name",
			cfg: RunConfig{
				Image: "alpine:latest",
			},
			exec: func(_ context.Context, args ...string) (string, error) {
				return "image not found\n", errors.New("exit 1")
			},
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &cliRuntime{binary: "test", exec: tt.exec}
			got, err := c.Run(context.Background(), tt.cfg)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			if tt.wantAnyErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCLIRuntimeStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		exec    execFunc
		wantErr bool
	}{
		{
			name: "success",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "container-name\n", nil
			},
		},
		{
			name: "error wraps with binary and container name",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "not found\n", errors.New("exit 1")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &cliRuntime{binary: "test", exec: tt.exec}
			err := c.Start(context.Background(), "my-container")
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "test start my-container")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCLIRuntimeStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		exec    execFunc
		wantErr bool
	}{
		{
			name: "success",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "", nil
			},
		},
		{
			name: "error wraps with binary and container name",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "timeout\n", errors.New("exit 1")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &cliRuntime{binary: "test", exec: tt.exec}
			err := c.Stop(context.Background(), "my-container")
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "test stop my-container")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCLIRuntimeRemove(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		exec    execFunc
		wantErr bool
	}{
		{
			name: "success",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "", nil
			},
		},
		{
			name: "error wraps with binary and container name",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "permission denied\n", errors.New("exit 1")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &cliRuntime{binary: "test", exec: tt.exec}
			err := c.Remove(context.Background(), "my-container")
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "test rm my-container")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCLIRuntimeInspect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		exec       execFunc
		wantState  ContainerState
		wantErr    error
		wantAnyErr bool
	}{
		{
			name: "running container",
			exec: func(_ context.Context, args ...string) (string, error) {
				return `{"Status":"running"}`, nil
			},
			wantState: StateRunning,
		},
		{
			name: "exited container",
			exec: func(_ context.Context, args ...string) (string, error) {
				return `{"Status":"exited"}`, nil
			},
			wantState: StateExited,
		},
		{
			name: "created container",
			exec: func(_ context.Context, args ...string) (string, error) {
				return `{"Status":"created"}`, nil
			},
			wantState: StateCreated,
		},
		{
			name: "missing container returns ErrContainerNotFound",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "Error: No such container: foo", errors.New("exit 1")
			},
			wantState: StateUnknown,
			wantErr:   ErrContainerNotFound,
		},
		{
			name: "no such object returns ErrContainerNotFound",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "Error: No such object: foo", errors.New("exit 1")
			},
			wantState: StateUnknown,
			wantErr:   ErrContainerNotFound,
		},
		{
			name: "unexpected error propagates",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "permission denied", errors.New("exit 1")
			},
			wantAnyErr: true,
		},
		{
			name: "invalid JSON propagates error",
			exec: func(_ context.Context, args ...string) (string, error) {
				return "not-json", nil
			},
			wantAnyErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := &cliRuntime{binary: "test", exec: tt.exec}
			state, err := c.Inspect(context.Background(), "my-container")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, tt.wantState, state)
				return
			}
			if tt.wantAnyErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantState, state)
		})
	}
}

func TestBuildRunArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     RunConfig
		want    []string
		wantErr error
	}{
		{
			name: "minimal config",
			cfg: RunConfig{
				Image: "alpine:latest",
			},
			want: []string{"run", "alpine:latest"},
		},
		{
			name: "full config",
			cfg: RunConfig{
				Name:       "my-container",
				Image:      "moby/buildkit:latest",
				Privileged: true,
				Detach:     true,
				Ports: []PortBinding{
					{HostAddr: "127.0.0.1", HostPort: "1234", ContPort: "1234"},
				},
				Volumes: []VolumeMount{
					{Name: "my-vol", Target: "/var/lib/data"},
				},
				Args: []string{"--addr", "tcp://0.0.0.0:1234"},
			},
			want: []string{
				"run", "-d",
				"--name", "my-container",
				"--privileged",
				"-p", "127.0.0.1:1234:1234",
				"-v", "my-vol:/var/lib/data",
				"moby/buildkit:latest",
				"--addr", "tcp://0.0.0.0:1234",
			},
		},
		{
			name: "multiple ports and volumes",
			cfg: RunConfig{
				Image: "nginx",
				Ports: []PortBinding{
					{HostAddr: "0.0.0.0", HostPort: "80", ContPort: "80"},
					{HostAddr: "0.0.0.0", HostPort: "443", ContPort: "443"},
				},
				Volumes: []VolumeMount{
					{Name: "conf", Target: "/etc/nginx"},
					{Name: "data", Target: "/usr/share/nginx/html"},
				},
			},
			want: []string{
				"run",
				"-p", "0.0.0.0:80:80",
				"-p", "0.0.0.0:443:443",
				"-v", "conf:/etc/nginx",
				"-v", "data:/usr/share/nginx/html",
				"nginx",
			},
		},
		{
			name: "port without HostAddr omits host binding",
			cfg: RunConfig{
				Image: "nginx",
				Ports: []PortBinding{
					{HostPort: "8080", ContPort: "80"},
				},
			},
			want: []string{"run", "-p", "8080:80", "nginx"},
		},
		{
			name: "empty HostPort returns ErrInvalidPort",
			cfg: RunConfig{
				Image: "nginx",
				Ports: []PortBinding{{HostAddr: "0.0.0.0", ContPort: "80"}},
			},
			wantErr: ErrInvalidPort,
		},
		{
			name: "empty ContPort returns ErrInvalidPort",
			cfg: RunConfig{
				Image: "nginx",
				Ports: []PortBinding{{HostPort: "80"}},
			},
			wantErr: ErrInvalidPort,
		},
		{
			name: "empty volume name returns ErrInvalidVolume",
			cfg: RunConfig{
				Image:   "nginx",
				Volumes: []VolumeMount{{Target: "/data"}},
			},
			wantErr: ErrInvalidVolume,
		},
		{
			name: "empty volume target returns ErrInvalidVolume",
			cfg: RunConfig{
				Image:   "nginx",
				Volumes: []VolumeMount{{Name: "vol"}},
			},
			wantErr: ErrInvalidVolume,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildRunArgs(tt.cfg)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  ContainerState
	}{
		{name: "running", input: "running", want: StateRunning},
		{name: "Running mixed case", input: "Running", want: StateRunning},
		{name: "exited", input: "exited", want: StateExited},
		{name: "created", input: "created", want: StateCreated},
		{name: "paused", input: "paused", want: StatePaused},
		{name: "unknown value", input: "restarting", want: StateUnknown},
		{name: "empty string", input: "", want: StateUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseState(tt.input))
		})
	}
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "no such container", output: "Error: No such container: foo", want: true},
		{name: "no such object", output: "Error: No such object: foo", want: true},
		{name: "lowercase no such", output: "error: no such container: bar", want: true},
		{name: "permission denied", output: "permission denied", want: false},
		{name: "empty output", output: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isNotFound(tt.output))
		})
	}
}
