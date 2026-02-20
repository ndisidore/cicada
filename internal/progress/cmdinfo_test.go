package progress_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
			assert.Equal(t, tt.expected, progress.ExitInfo(tt.errStr), "ExitInfo(%q)", tt.errStr)
		})
	}
}
