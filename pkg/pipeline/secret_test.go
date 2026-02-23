package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pm "github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

func TestValidateSecretDecls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		decls   []pm.SecretDecl
		wantErr error
	}{
		{
			name:  "nil decls passes",
			decls: nil,
		},
		{
			name:  "empty decls passes",
			decls: []pm.SecretDecl{},
		},
		{
			name: "valid hostEnv source passes",
			decls: []pm.SecretDecl{
				{Name: "GH_TOKEN", Source: pm.SecretSourceHostEnv},
			},
		},
		{
			name: "valid file source passes",
			decls: []pm.SecretDecl{
				{Name: "tls-cert", Source: pm.SecretSourceFile, Path: "/etc/ssl/cert.pem"},
			},
		},
		{
			name: "valid cmd source passes",
			decls: []pm.SecretDecl{
				{Name: "vault-secret", Source: pm.SecretSourceCmd, Cmd: "vault kv get secret/app"},
			},
		},
		{
			name: "multiple valid decls pass",
			decls: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv},
				{Name: "cert", Source: pm.SecretSourceFile, Path: "/etc/cert.pem"},
				{Name: "vault", Source: pm.SecretSourceCmd, Cmd: "vault read"},
			},
		},
		{
			name: "empty name returns ErrEmptySecretName",
			decls: []pm.SecretDecl{
				{Name: "", Source: pm.SecretSourceHostEnv},
			},
			wantErr: pm.ErrEmptySecretName,
		},
		{
			name: "duplicate name returns ErrDuplicateSecret",
			decls: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv},
				{Name: "token", Source: pm.SecretSourceFile, Path: "/tmp/token"},
			},
			wantErr: pm.ErrDuplicateSecret,
		},
		{
			name: "invalid source returns ErrInvalidSecretSource",
			decls: []pm.SecretDecl{
				{Name: "bad", Source: pm.SecretSource("vault")},
			},
			wantErr: pm.ErrInvalidSecretSource,
		},
		{
			name: "empty source returns ErrInvalidSecretSource",
			decls: []pm.SecretDecl{
				{Name: "bad", Source: ""},
			},
			wantErr: pm.ErrInvalidSecretSource,
		},
		{
			name: "file source missing path returns ErrMissingSecretPath",
			decls: []pm.SecretDecl{
				{Name: "cert", Source: pm.SecretSourceFile, Path: ""},
			},
			wantErr: pm.ErrMissingSecretPath,
		},
		{
			name: "file source whitespace-only path returns ErrMissingSecretPath",
			decls: []pm.SecretDecl{
				{Name: "cert", Source: pm.SecretSourceFile, Path: "   "},
			},
			wantErr: pm.ErrMissingSecretPath,
		},
		{
			name: "cmd source missing cmd returns ErrMissingSecretCmd",
			decls: []pm.SecretDecl{
				{Name: "vault", Source: pm.SecretSourceCmd, Cmd: ""},
			},
			wantErr: pm.ErrMissingSecretCmd,
		},
		{
			name: "cmd source whitespace-only cmd returns ErrMissingSecretCmd",
			decls: []pm.SecretDecl{
				{Name: "vault", Source: pm.SecretSourceCmd, Cmd: "   "},
			},
			wantErr: pm.ErrMissingSecretCmd,
		},
		{
			name: "var with file source returns ErrSecretVarWithNonHostEnv",
			decls: []pm.SecretDecl{
				{Name: "cert", Source: pm.SecretSourceFile, Path: "/etc/cert.pem", Var: "CERT_VAR"},
			},
			wantErr: pm.ErrSecretVarWithNonHostEnv,
		},
		{
			name: "var with cmd source returns ErrSecretVarWithNonHostEnv",
			decls: []pm.SecretDecl{
				{Name: "vault", Source: pm.SecretSourceCmd, Cmd: "vault read", Var: "VAULT_VAR"},
			},
			wantErr: pm.ErrSecretVarWithNonHostEnv,
		},
		{
			name: "path with hostEnv source returns ErrSecretPathWithNonFile",
			decls: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv, Path: "/etc/secret"},
			},
			wantErr: pm.ErrSecretPathWithNonFile,
		},
		{
			name: "path with cmd source returns ErrSecretPathWithNonFile",
			decls: []pm.SecretDecl{
				{Name: "vault", Source: pm.SecretSourceCmd, Cmd: "vault read", Path: "/stray"},
			},
			wantErr: pm.ErrSecretPathWithNonFile,
		},
		{
			name: "cmd with hostEnv source returns ErrSecretCmdWithNonCmd",
			decls: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv, Cmd: "echo stray"},
			},
			wantErr: pm.ErrSecretCmdWithNonCmd,
		},
		{
			name: "cmd with file source returns ErrSecretCmdWithNonCmd",
			decls: []pm.SecretDecl{
				{Name: "cert", Source: pm.SecretSourceFile, Path: "/etc/cert.pem", Cmd: "stray"},
			},
			wantErr: pm.ErrSecretCmdWithNonCmd,
		},
		{
			name: "hostEnv with invalid Name and no Var returns ErrInvalidSecretHostEnvName",
			decls: []pm.SecretDecl{
				{Name: "gh-token", Source: pm.SecretSourceHostEnv},
			},
			wantErr: pm.ErrInvalidSecretHostEnvName,
		},
		{
			name: "hostEnv with valid Var overrides invalid Name",
			decls: []pm.SecretDecl{
				{Name: "gh-token", Source: pm.SecretSourceHostEnv, Var: "GITHUB_TOKEN"},
			},
		},
		{
			name: "hostEnv with invalid Var returns ErrInvalidSecretHostEnvName",
			decls: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv, Var: "invalid-var"},
			},
			wantErr: pm.ErrInvalidSecretHostEnvName,
		},
		{
			name: "first error wins for name before source",
			decls: []pm.SecretDecl{
				{Name: "good", Source: pm.SecretSourceHostEnv},
				{Name: "", Source: pm.SecretSource("bogus")},
			},
			wantErr: pm.ErrEmptySecretName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act
			err := validateSecretDecls("test", tt.decls)

			// Assert
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateSecretRefs(t *testing.T) {
	t.Parallel()

	declared := map[string]struct{}{
		"token": {},
		"cert":  {},
		"vault": {},
	}

	tests := []struct {
		name     string
		refs     []pm.SecretRef
		declared map[string]struct{}
		wantErr  error
	}{
		{
			name:     "nil refs passes",
			refs:     nil,
			declared: declared,
		},
		{
			name:     "empty refs passes",
			refs:     []pm.SecretRef{},
			declared: declared,
		},
		{
			name: "valid env ref passes",
			refs: []pm.SecretRef{
				{Name: "token", Env: "GITHUB_TOKEN"},
			},
			declared: declared,
		},
		{
			name: "valid mount ref passes",
			refs: []pm.SecretRef{
				{Name: "cert", Mount: "/run/secrets/cert.pem"},
			},
			declared: declared,
		},
		{
			name: "multiple valid refs pass",
			refs: []pm.SecretRef{
				{Name: "token", Env: "GH_TOKEN"},
				{Name: "cert", Mount: "/run/secrets/cert"},
			},
			declared: declared,
		},
		{
			name: "empty ref name returns ErrEmptySecretName",
			refs: []pm.SecretRef{
				{Name: "", Env: "FOO"},
			},
			declared: declared,
			wantErr:  pm.ErrEmptySecretName,
		},
		{
			name: "undeclared secret returns ErrUnknownSecret",
			refs: []pm.SecretRef{
				{Name: "nonexistent", Env: "SECRET"},
			},
			declared: declared,
			wantErr:  pm.ErrUnknownSecret,
		},
		{
			name: "duplicate ref returns ErrDuplicateSecretRef",
			refs: []pm.SecretRef{
				{Name: "token", Env: "TOKEN_A"},
				{Name: "token", Env: "TOKEN_B"},
			},
			declared: declared,
			wantErr:  pm.ErrDuplicateSecretRef,
		},
		{
			name: "no env or mount returns ErrSecretRefNoTarget",
			refs: []pm.SecretRef{
				{Name: "token"},
			},
			declared: declared,
			wantErr:  pm.ErrSecretRefNoTarget,
		},
		{
			name: "both env and mount returns ErrSecretRefBothTarget",
			refs: []pm.SecretRef{
				{Name: "token", Env: "TOKEN", Mount: "/run/secrets/token"},
			},
			declared: declared,
			wantErr:  pm.ErrSecretRefBothTarget,
		},
		{
			name: "relative mount path returns ErrRelativeSecretMount",
			refs: []pm.SecretRef{
				{Name: "token", Mount: "run/secrets/token"},
			},
			declared: declared,
			wantErr:  pm.ErrRelativeSecretMount,
		},
		{
			name: "invalid env name returns ErrInvalidSecretEnvName",
			refs: []pm.SecretRef{
				{Name: "token", Env: "1INVALID"},
			},
			declared: declared,
			wantErr:  pm.ErrInvalidSecretEnvName,
		},
		{
			name: "env name with hyphens returns ErrInvalidSecretEnvName",
			refs: []pm.SecretRef{
				{Name: "token", Env: "MY-TOKEN"},
			},
			declared: declared,
			wantErr:  pm.ErrInvalidSecretEnvName,
		},
		{
			name: "env name with dots returns ErrInvalidSecretEnvName",
			refs: []pm.SecretRef{
				{Name: "token", Env: "my.token"},
			},
			declared: declared,
			wantErr:  pm.ErrInvalidSecretEnvName,
		},
		{
			name: "underscore-prefixed env name passes",
			refs: []pm.SecretRef{
				{Name: "token", Env: "_TOKEN_123"},
			},
			declared: declared,
		},
		{
			name: "duplicate env target returns ErrDuplicateSecretEnvName",
			refs: []pm.SecretRef{
				{Name: "token", Env: "API_KEY"},
				{Name: "cert", Env: "API_KEY"},
			},
			declared: declared,
			wantErr:  pm.ErrDuplicateSecretEnvName,
		},
		{
			name: "duplicate mount target returns ErrDuplicateSecretMount",
			refs: []pm.SecretRef{
				{Name: "token", Mount: "/run/secrets/key"},
				{Name: "cert", Mount: "/run/secrets/key"},
			},
			declared: declared,
			wantErr:  pm.ErrDuplicateSecretMount,
		},
		{
			name: "empty declared set rejects all refs",
			refs: []pm.SecretRef{
				{Name: "token", Env: "TOKEN"},
			},
			declared: map[string]struct{}{},
			wantErr:  pm.ErrUnknownSecret,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act
			_, _, err := validateSecretRefs("test", tt.refs, tt.declared)

			// Assert
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateSecretInheritance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		jobRefs  []pm.SecretRef
		stepRefs []pm.SecretRef
		wantErr  error
	}{
		{
			name:     "no overlap passes",
			jobRefs:  []pm.SecretRef{{Name: "token", Env: "TOKEN"}},
			stepRefs: []pm.SecretRef{{Name: "cert", Mount: "/run/secrets/cert"}},
		},
		{
			name:     "nil job refs passes",
			jobRefs:  nil,
			stepRefs: []pm.SecretRef{{Name: "token", Env: "TOKEN"}},
		},
		{
			name:     "nil step refs passes",
			jobRefs:  []pm.SecretRef{{Name: "token", Env: "TOKEN"}},
			stepRefs: nil,
		},
		{
			name:     "both nil passes",
			jobRefs:  nil,
			stepRefs: nil,
		},
		{
			name:     "both empty passes",
			jobRefs:  []pm.SecretRef{},
			stepRefs: []pm.SecretRef{},
		},
		{
			name:     "step overriding job secret returns ErrSecretOverride",
			jobRefs:  []pm.SecretRef{{Name: "token", Env: "TOKEN"}},
			stepRefs: []pm.SecretRef{{Name: "token", Mount: "/run/secrets/token"}},
			wantErr:  pm.ErrSecretOverride,
		},
		{
			name: "step overriding one of multiple job secrets returns ErrSecretOverride",
			jobRefs: []pm.SecretRef{
				{Name: "token", Env: "TOKEN"},
				{Name: "cert", Mount: "/run/secrets/cert"},
			},
			stepRefs: []pm.SecretRef{
				{Name: "cert", Env: "CERT_CONTENT"},
			},
			wantErr: pm.ErrSecretOverride,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Act
			err := validateSecretInheritance("test", tt.jobRefs, tt.stepRefs)

			// Assert
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSecretDeclSet(t *testing.T) {
	t.Parallel()

	t.Run("builds lookup set from decls", func(t *testing.T) {
		t.Parallel()

		decls := []pm.SecretDecl{
			{Name: "token", Source: pm.SecretSourceHostEnv},
			{Name: "cert", Source: pm.SecretSourceFile, Path: "/etc/cert.pem"},
			{Name: "vault", Source: pm.SecretSourceCmd, Cmd: "vault read"},
		}

		got := secretDeclSet(decls)

		require.Len(t, got, 3)
		assert.Contains(t, got, "token")
		assert.Contains(t, got, "cert")
		assert.Contains(t, got, "vault")
	})

	t.Run("nil decls returns empty set", func(t *testing.T) {
		t.Parallel()

		got := secretDeclSet(nil)

		assert.Empty(t, got)
	})

	t.Run("empty decls returns empty set", func(t *testing.T) {
		t.Parallel()

		got := secretDeclSet([]pm.SecretDecl{})

		assert.Empty(t, got)
	})
}

// validJob returns a minimal valid job for use in pipeline validation tests.
// Callers override fields as needed to test specific secret behaviors.
func validJob(name string) pm.Job {
	return pm.Job{
		Name:  name,
		Image: "alpine:latest",
		Steps: []pm.Step{{Name: "run", Run: []string{"echo ok"}}},
	}
}

func TestPipelineValidateSecrets(t *testing.T) {
	t.Parallel()

	t.Run("valid pipeline with secrets passes", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "gh-token", Env: "GITHUB_TOKEN"},
			{Name: "tls-cert", Mount: "/run/secrets/cert.pem"},
		}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "gh-token", Source: pm.SecretSourceHostEnv, Var: "GH_TOKEN"},
				{Name: "tls-cert", Source: pm.SecretSourceFile, Path: "/etc/ssl/cert.pem"},
				{Name: "db-pass", Source: pm.SecretSourceCmd, Cmd: "vault kv get db/pass"},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		order, err := Validate(&p)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []int{0}, order)
	})

	t.Run("valid pipeline with step-level secrets passes", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "gh-token", Env: "GITHUB_TOKEN"},
		}
		j.Steps = []pm.Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []pm.SecretRef{{Name: "tls-cert", Mount: "/run/secrets/cert"}},
		}}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "gh-token", Source: pm.SecretSourceHostEnv, Var: "GH_TOKEN"},
				{Name: "tls-cert", Source: pm.SecretSourceFile, Path: "/etc/cert.pem"},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		order, err := Validate(&p)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []int{0}, order)
	})

	t.Run("unknown secret ref in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "nonexistent", Env: "SECRET"},
		}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "gh-token", Source: pm.SecretSourceHostEnv, Var: "GH_TOKEN"},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrUnknownSecret)
	})

	t.Run("unknown secret ref in step fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Steps = []pm.Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []pm.SecretRef{{Name: "missing", Env: "SECRET"}},
		}}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "gh-token", Source: pm.SecretSourceHostEnv, Var: "GH_TOKEN"},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrUnknownSecret)
	})

	t.Run("step overriding job secret fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "gh-token", Env: "GITHUB_TOKEN"},
		}
		j.Steps = []pm.Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []pm.SecretRef{{Name: "gh-token", Mount: "/run/secrets/token"}},
		}}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "gh-token", Source: pm.SecretSourceHostEnv, Var: "GH_TOKEN"},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrSecretOverride)
	})

	t.Run("invalid pipeline-level secret decl fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("build")
		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "", Source: pm.SecretSourceHostEnv},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrEmptySecretName)
	})

	t.Run("duplicate secret ref in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "token", Env: "TOKEN_A"},
			{Name: "token", Env: "TOKEN_B"},
		}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrDuplicateSecretRef)
	})

	t.Run("secret ref with no target in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "token"},
		}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrSecretRefNoTarget)
	})

	t.Run("secret ref with both targets in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "token", Env: "TOKEN", Mount: "/run/secrets/token"},
		}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrSecretRefBothTarget)
	})

	t.Run("relative mount path in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "token", Mount: "relative/path"},
		}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrRelativeSecretMount)
	})

	t.Run("invalid env name in job secret ref fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "token", Env: "1INVALID"},
		}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "token", Source: pm.SecretSourceHostEnv, Var: "VALID_VAR"},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrInvalidSecretEnvName)
	})

	t.Run("step env colliding with job env target fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "token_a", Env: "TOKEN"},
		}
		j.Steps = []pm.Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []pm.SecretRef{{Name: "token_b", Env: "TOKEN"}},
		}}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "token_a", Source: pm.SecretSourceHostEnv},
				{Name: "token_b", Source: pm.SecretSourceHostEnv},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrDuplicateSecretEnvName)
	})

	t.Run("step mount colliding with job mount target fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []pm.SecretRef{
			{Name: "cert_a", Mount: "/run/secrets/cert"},
		}
		j.Steps = []pm.Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []pm.SecretRef{{Name: "cert_b", Mount: "/run/secrets/cert"}},
		}}

		p := pm.Pipeline{
			Name: "test",
			Secrets: []pm.SecretDecl{
				{Name: "cert_a", Source: pm.SecretSourceFile, Path: "/etc/cert_a.pem"},
				{Name: "cert_b", Source: pm.SecretSourceFile, Path: "/etc/cert_b.pem"},
			},
			Jobs: []pm.Job{j},
		}

		// Act
		_, err := Validate(&p)

		// Assert
		require.ErrorIs(t, err, pm.ErrDuplicateSecretMount)
	})

	t.Run("pipeline with no secrets and no refs passes", func(t *testing.T) {
		t.Parallel()

		// Arrange
		p := pm.Pipeline{
			Name: "test",
			Jobs: []pm.Job{validJob("build")},
		}

		// Act
		order, err := Validate(&p)

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []int{0}, order)
	})
}
