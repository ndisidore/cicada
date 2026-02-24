package secret

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// t.Parallel omitted: t.Setenv mutates the process environment.
func TestResolve_hostEnv(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string // value to set; left unset when empty and unsetEnv is true
		unset   bool
		decl    pipelinemodel.SecretDecl
		wantVal string
		wantErr bool
	}{
		{
			name:   "reads env var matching Name",
			envVal: "s3cr3t",
			decl: pipelinemodel.SecretDecl{
				Name:   "MY_SECRET",
				Source: pipelinemodel.SecretSourceHostEnv,
			},
			wantVal: "s3cr3t",
		},
		{
			name:  "unset env var returns error",
			unset: true,
			decl: pipelinemodel.SecretDecl{
				Name:   "MISSING_VAR",
				Source: pipelinemodel.SecretSourceHostEnv,
			},
			wantErr: true,
		},
		{
			name:   "Var overrides Name for env lookup",
			envVal: "from-override",
			decl: pipelinemodel.SecretDecl{
				Name:   "secret_name",
				Source: pipelinemodel.SecretSourceHostEnv,
				Var:    "CICADA_TEST_OVERRIDE",
			},
			wantVal: "from-override",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: t.Setenv forbids parallel subtests because
			// it mutates shared process-level env and restores it on cleanup.
			// For the "unset" case, t.Setenv registers the original value
			// for cleanup, then os.Unsetenv removes it so LookupEnv fails.
			envKey := tt.decl.Var
			if envKey == "" {
				envKey = tt.decl.Name
			}
			if tt.unset {
				t.Setenv(envKey, "")
				os.Unsetenv(envKey)
			} else {
				t.Setenv(envKey, tt.envVal)
			}

			got, err := Resolve(context.Background(), []pipelinemodel.SecretDecl{tt.decl})
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, pipelinemodel.ErrSecretResolution)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tt.decl.Name, got[0].Name)
			assert.Equal(t, tt.wantVal, string(got[0].Value))
		})
	}
}

func TestResolve_file(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		missingFn bool // when true, use a non-existent path
		wantErr   bool
	}{
		{
			name:    "reads file contents",
			content: "file-secret-value",
		},
		{
			name:    "reads empty file",
			content: "",
		},
		{
			name:      "missing file returns error",
			missingFn: true,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			var path string
			if tt.missingFn {
				path = filepath.Join(dir, "no-such-file")
			} else {
				path = filepath.Join(dir, "secret.txt")
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			}

			decl := pipelinemodel.SecretDecl{
				Name:   "file_secret",
				Source: pipelinemodel.SecretSourceFile,
				Path:   path,
			}

			got, err := Resolve(context.Background(), []pipelinemodel.SecretDecl{decl})
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, pipelinemodel.ErrSecretResolution)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, decl.Name, got[0].Name)
			assert.Equal(t, tt.content, string(got[0].Value))
		})
	}
}

func TestResolve_cmd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cmd     string
		wantVal string
		wantErr bool
	}{
		{
			name:    "echo trims trailing newline",
			cmd:     "echo hello",
			wantVal: "hello",
		},
		{
			name:    "multi-line output trims only trailing newlines",
			cmd:     "printf 'line1\nline2\n'",
			wantVal: "line1\nline2",
		},
		{
			name:    "bad command returns error",
			cmd:     "false",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decl := pipelinemodel.SecretDecl{
				Name:   "cmd_secret",
				Source: pipelinemodel.SecretSourceCmd,
				Cmd:    tt.cmd,
			}

			got, err := Resolve(context.Background(), []pipelinemodel.SecretDecl{decl})
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, pipelinemodel.ErrSecretResolution)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, decl.Name, got[0].Name)
			assert.Equal(t, tt.wantVal, string(got[0].Value))
		})
	}
}

func TestResolve_unknownSource(t *testing.T) {
	t.Parallel()

	decl := pipelinemodel.SecretDecl{
		Name:   "bad",
		Source: pipelinemodel.SecretSource("vault"),
	}

	_, err := Resolve(context.Background(), []pipelinemodel.SecretDecl{decl})
	require.ErrorIs(t, err, pipelinemodel.ErrSecretResolution)
	assert.Contains(t, err.Error(), "unknown source")
}

func TestResolve_emptyDecls(t *testing.T) {
	t.Parallel()

	got, err := Resolve(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestResolve_cancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	decl := pipelinemodel.SecretDecl{
		Name:   "ctx_secret",
		Source: pipelinemodel.SecretSourceCmd,
		Cmd:    "sleep 10",
	}

	_, err := Resolve(ctx, []pipelinemodel.SecretDecl{decl})
	require.Error(t, err)
	assert.ErrorIs(t, err, pipelinemodel.ErrSecretResolution)
}

// t.Parallel omitted: t.Setenv mutates the process environment.
func TestResolve_multipleDecls(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("file-val"), 0o600))

	t.Setenv("CICADA_MULTI_TEST", "env-val")

	decls := []pipelinemodel.SecretDecl{
		{Name: "CICADA_MULTI_TEST", Source: pipelinemodel.SecretSourceHostEnv},
		{Name: "from_file", Source: pipelinemodel.SecretSourceFile, Path: filePath},
		{Name: "from_cmd", Source: pipelinemodel.SecretSourceCmd, Cmd: "echo cmd-val"},
	}

	got, err := Resolve(context.Background(), decls)
	require.NoError(t, err)
	require.Len(t, got, 3)

	assert.Equal(t, "CICADA_MULTI_TEST", got[0].Name)
	assert.Equal(t, "env-val", string(got[0].Value))

	assert.Equal(t, "from_file", got[1].Name)
	assert.Equal(t, "file-val", string(got[1].Value))

	assert.Equal(t, "from_cmd", got[2].Name)
	assert.Equal(t, "cmd-val", string(got[2].Value))
}

func TestToMap(t *testing.T) {
	t.Parallel()

	secrets := []ResolvedSecret{
		{Name: "a", Value: []byte("val-a")},
		{Name: "b", Value: []byte("val-b")},
	}

	m := ToMap(secrets)
	require.Len(t, m, 2)
	assert.Equal(t, []byte("val-a"), m["a"])
	assert.Equal(t, []byte("val-b"), m["b"])
}

func TestToMap_empty(t *testing.T) {
	t.Parallel()

	m := ToMap(nil)
	assert.Empty(t, m)
}

func TestPlaintextValues(t *testing.T) {
	t.Parallel()

	secrets := []ResolvedSecret{
		{Name: "has_val", Value: []byte("visible")},
		{Name: "empty", Value: []byte("")},
		{Name: "also_has_val", Value: []byte("also-visible")},
	}

	m := PlaintextValues(secrets)
	require.Len(t, m, 2)
	assert.Equal(t, "visible", m["has_val"])
	assert.Equal(t, "also-visible", m["also_has_val"])
	_, exists := m["empty"]
	assert.False(t, exists)
}

func TestPlaintextValues_empty(t *testing.T) {
	t.Parallel()

	m := PlaintextValues(nil)
	assert.Empty(t, m)
}

func TestExpandHome(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "tilde prefix expands",
			path: "~/some/path",
			want: home + "/some/path",
		},
		{
			name: "absolute path unchanged",
			path: "/etc/secret",
			want: "/etc/secret",
		},
		{
			name: "relative path unchanged",
			path: "relative/path",
			want: "relative/path",
		},
		{
			name: "bare tilde expands to home",
			path: "~",
			want: home,
		},
		{
			name: "tilde not at start unchanged",
			path: "/foo/~/bar",
			want: "/foo/~/bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, expandHome(tt.path))
		})
	}
}
