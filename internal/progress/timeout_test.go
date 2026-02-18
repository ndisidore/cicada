package progress

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIsTimeoutExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		cfgTimeout time.Duration
		want       bool
	}{
		{
			name:       "exit 124 with timeout configured",
			input:      `process "..." did not complete successfully: exit code: 124`,
			cfgTimeout: 3 * time.Second,
			want:       true,
		},
		{
			name:       "exit 137 with timeout configured (BusyBox timeout -s KILL)",
			input:      `process "..." did not complete successfully: exit code: 137`,
			cfgTimeout: 3 * time.Second,
			want:       true,
		},
		{
			name:       "exit 124 without timeout configured",
			input:      "exit code: 124",
			cfgTimeout: 0,
			want:       false,
		},
		{
			name:       "exit 137 without timeout configured (OOM/external kill)",
			input:      "exit code: 137",
			cfgTimeout: 0,
			want:       false,
		},
		{
			name:       "non-matching exit code",
			input:      "exit code: 1",
			cfgTimeout: 3 * time.Second,
			want:       false,
		},
		{
			name:       "trailing whitespace",
			input:      "exit code: 124 ",
			cfgTimeout: 3 * time.Second,
			want:       true,
		},
		{
			name:       "non-matching 137-like code",
			input:      "exit code: 1137",
			cfgTimeout: 3 * time.Second,
			want:       false,
		},
		{
			name:       "empty string",
			input:      "",
			cfgTimeout: 3 * time.Second,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, IsTimeoutExitCode(tt.input, tt.cfgTimeout))
		})
	}
}
