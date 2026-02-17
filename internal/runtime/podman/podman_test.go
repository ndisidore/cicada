package podman

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ndisidore/cicada/internal/runtime"
	"github.com/ndisidore/cicada/internal/runtime/cliutil"
)

func TestType(t *testing.T) {
	t.Parallel()
	assert.Equal(t, runtime.Podman, New().Type())
}

func TestAvailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		exec cliutil.ExecFunc
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
			p := &Runtime{CLIRuntime: &cliutil.CLIRuntime{Binary: "podman", Exec: tt.exec}}
			assert.Equal(t, tt.want, p.Available(context.Background()))
		})
	}
}
