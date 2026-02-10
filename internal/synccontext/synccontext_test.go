package synccontext

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadIgnorePatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		files   map[string]string // filename -> content
		setup   func(t *testing.T, dir string)
		want    []string
		wantErr error
	}{
		{
			name: "cicadaignore takes precedence",
			files: map[string]string{
				".cicadaignore": "*.log\nbuild/\n",
				".dockerignore": "*.tmp\n",
			},
			want: []string{"*.log", "build/"},
		},
		{
			name: "falls back to dockerignore",
			files: map[string]string{
				".dockerignore": "node_modules\n.git\n",
			},
			want: []string{"node_modules", ".git"},
		},
		{
			name:    "no ignore files returns ErrNoIgnoreFile",
			files:   nil,
			wantErr: ErrNoIgnoreFile,
		},
		{
			name: "blank lines and comments skipped",
			files: map[string]string{
				".cicadaignore": "# this is a comment\n\n*.log\n\n# another\ntmp/\n",
			},
			want: []string{"*.log", "tmp/"},
		},
		{
			name: "whitespace trimmed",
			files: map[string]string{
				".cicadaignore": "  *.o  \n  dist/  \n",
			},
			want: []string{"*.o", "dist/"},
		},
		{
			name:  "unreadable cicadaignore returns error",
			files: map[string]string{".cicadaignore": ""},
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("permission test not supported on windows")
				}
				require.NoError(t, os.Chmod(filepath.Join(dir, ".cicadaignore"), 0o000))
			},
			wantErr: os.ErrPermission,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			for name, content := range tt.files {
				err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
				require.NoError(t, err)
			}
			if tt.setup != nil {
				tt.setup(t, dir)
			}

			got, err := LoadIgnorePatterns(dir)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
