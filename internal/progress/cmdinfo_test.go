package progress_test

import (
	"testing"

	"github.com/ndisidore/cicada/internal/progress"
)

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
			got := progress.ExitInfo(tt.errStr)
			if got != tt.expected {
				t.Errorf("ExitInfo(%q) = %q, want %q", tt.errStr, got, tt.expected)
			}
		})
	}
}
