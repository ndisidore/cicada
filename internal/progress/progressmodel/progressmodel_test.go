package progressmodel

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

func TestExitInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		errStr   string
		expected string
	}{
		{
			name:     "standard exit code 1",
			errStr:   `process "/bin/sh -c echo test" did not complete successfully: exit code: 1`,
			expected: "exit code: 1",
		},
		{
			name:     "exit code 137 with preamble",
			errStr:   "process \"/bin/sh -c preamble\\necho test\" did not complete successfully: exit code: 137",
			expected: "exit code: 137",
		},
		{
			name:     "nested failed to solve wrapper",
			errStr:   `failed to solve: process "/bin/sh -c cmd" did not complete successfully: exit code: 2`,
			expected: "exit code: 2",
		},
		{
			name:     "marker at end returns empty string",
			errStr:   `process "/bin/sh -c cmd" did not complete successfully: `,
			expected: "",
		},
		{
			name:     "double marker uses last occurrence",
			errStr:   `did not complete successfully: process "/bin/sh -c cmd" did not complete successfully: exit code: 9`,
			expected: "exit code: 9",
		},
		{
			name:     "non-exit-code suffix after marker",
			errStr:   `process "/bin/sh -c cmd" did not complete successfully: context deadline exceeded`,
			expected: "context deadline exceeded",
		},
		{
			name:     "unrelated error",
			errStr:   "some other error",
			expected: "some other error",
		},
		{
			name:     "empty string",
			errStr:   "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, ExitInfo(tt.errStr), "ExitInfo(%q)", tt.errStr)
		})
	}
}
