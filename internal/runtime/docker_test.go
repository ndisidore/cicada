//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDockerType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Docker, NewDocker().Type())
}

func TestDockerAvailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exec execFunc
		want bool
	}{
		{
			name: "daemon responsive",
			exec: func(_ context.Context, _ ...string) (string, error) {
				return `"24.0.7"`, nil
			},
			want: true,
		},
		{
			name: "daemon unresponsive",
			exec: func(_ context.Context, _ ...string) (string, error) {
				return "Cannot connect to the Docker daemon", errors.New("exit 1")
			},
			want: false,
		},
		{
			name: "binary not found",
			exec: func(_ context.Context, _ ...string) (string, error) {
				return "", errors.New("executable file not found")
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := &DockerRuntime{cliRuntime: &cliRuntime{binary: "docker", exec: tt.exec}}
			assert.Equal(t, tt.want, d.Available(context.Background()))
		})
	}
}
