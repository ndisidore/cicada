//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPodmanType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Podman, NewPodman().Type())
}

func TestPodmanAvailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exec execFunc
		want bool
	}{
		{
			name: "engine responsive",
			exec: func(_ context.Context, _ ...string) (string, error) {
				return `"linux"`, nil
			},
			want: true,
		},
		{
			name: "machine not running",
			exec: func(_ context.Context, _ ...string) (string, error) {
				return "cannot connect to Podman", errors.New("exit 125")
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
			p := &PodmanRuntime{cliRuntime: &cliRuntime{binary: "podman", exec: tt.exec}}
			assert.Equal(t, tt.want, p.Available(context.Background()))
		})
	}
}
