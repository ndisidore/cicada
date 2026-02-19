package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSecretDecls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		decls   []SecretDecl
		wantErr error
	}{
		{
			name:  "nil decls passes",
			decls: nil,
		},
		{
			name:  "empty decls passes",
			decls: []SecretDecl{},
		},
		{
			name: "valid hostEnv source passes",
			decls: []SecretDecl{
				{Name: "GH_TOKEN", Source: SecretSourceHostEnv},
			},
		},
		{
			name: "valid file source passes",
			decls: []SecretDecl{
				{Name: "tls-cert", Source: SecretSourceFile, Path: "/etc/ssl/cert.pem"},
			},
		},
		{
			name: "valid cmd source passes",
			decls: []SecretDecl{
				{Name: "vault-secret", Source: SecretSourceCmd, Cmd: "vault kv get secret/app"},
			},
		},
		{
			name: "multiple valid decls pass",
			decls: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv},
				{Name: "cert", Source: SecretSourceFile, Path: "/etc/cert.pem"},
				{Name: "vault", Source: SecretSourceCmd, Cmd: "vault read"},
			},
		},
		{
			name: "empty name returns ErrEmptySecretName",
			decls: []SecretDecl{
				{Name: "", Source: SecretSourceHostEnv},
			},
			wantErr: ErrEmptySecretName,
		},
		{
			name: "duplicate name returns ErrDuplicateSecret",
			decls: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv},
				{Name: "token", Source: SecretSourceFile, Path: "/tmp/token"},
			},
			wantErr: ErrDuplicateSecret,
		},
		{
			name: "invalid source returns ErrInvalidSecretSource",
			decls: []SecretDecl{
				{Name: "bad", Source: SecretSource("vault")},
			},
			wantErr: ErrInvalidSecretSource,
		},
		{
			name: "empty source returns ErrInvalidSecretSource",
			decls: []SecretDecl{
				{Name: "bad", Source: ""},
			},
			wantErr: ErrInvalidSecretSource,
		},
		{
			name: "file source missing path returns ErrMissingSecretPath",
			decls: []SecretDecl{
				{Name: "cert", Source: SecretSourceFile, Path: ""},
			},
			wantErr: ErrMissingSecretPath,
		},
		{
			name: "file source whitespace-only path returns ErrMissingSecretPath",
			decls: []SecretDecl{
				{Name: "cert", Source: SecretSourceFile, Path: "   "},
			},
			wantErr: ErrMissingSecretPath,
		},
		{
			name: "cmd source missing cmd returns ErrMissingSecretCmd",
			decls: []SecretDecl{
				{Name: "vault", Source: SecretSourceCmd, Cmd: ""},
			},
			wantErr: ErrMissingSecretCmd,
		},
		{
			name: "cmd source whitespace-only cmd returns ErrMissingSecretCmd",
			decls: []SecretDecl{
				{Name: "vault", Source: SecretSourceCmd, Cmd: "   "},
			},
			wantErr: ErrMissingSecretCmd,
		},
		{
			name: "var with file source returns ErrSecretVarWithNonHostEnv",
			decls: []SecretDecl{
				{Name: "cert", Source: SecretSourceFile, Path: "/etc/cert.pem", Var: "CERT_VAR"},
			},
			wantErr: ErrSecretVarWithNonHostEnv,
		},
		{
			name: "var with cmd source returns ErrSecretVarWithNonHostEnv",
			decls: []SecretDecl{
				{Name: "vault", Source: SecretSourceCmd, Cmd: "vault read", Var: "VAULT_VAR"},
			},
			wantErr: ErrSecretVarWithNonHostEnv,
		},
		{
			name: "path with hostEnv source returns ErrSecretPathWithNonFile",
			decls: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv, Path: "/etc/secret"},
			},
			wantErr: ErrSecretPathWithNonFile,
		},
		{
			name: "path with cmd source returns ErrSecretPathWithNonFile",
			decls: []SecretDecl{
				{Name: "vault", Source: SecretSourceCmd, Cmd: "vault read", Path: "/stray"},
			},
			wantErr: ErrSecretPathWithNonFile,
		},
		{
			name: "cmd with hostEnv source returns ErrSecretCmdWithNonCmd",
			decls: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv, Cmd: "echo stray"},
			},
			wantErr: ErrSecretCmdWithNonCmd,
		},
		{
			name: "cmd with file source returns ErrSecretCmdWithNonCmd",
			decls: []SecretDecl{
				{Name: "cert", Source: SecretSourceFile, Path: "/etc/cert.pem", Cmd: "stray"},
			},
			wantErr: ErrSecretCmdWithNonCmd,
		},
		{
			name: "hostEnv with invalid Name and no Var returns ErrInvalidSecretHostEnvName",
			decls: []SecretDecl{
				{Name: "gh-token", Source: SecretSourceHostEnv},
			},
			wantErr: ErrInvalidSecretHostEnvName,
		},
		{
			name: "hostEnv with valid Var overrides invalid Name",
			decls: []SecretDecl{
				{Name: "gh-token", Source: SecretSourceHostEnv, Var: "GITHUB_TOKEN"},
			},
		},
		{
			name: "hostEnv with invalid Var returns ErrInvalidSecretHostEnvName",
			decls: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv, Var: "invalid-var"},
			},
			wantErr: ErrInvalidSecretHostEnvName,
		},
		{
			name: "first error wins for name before source",
			decls: []SecretDecl{
				{Name: "good", Source: SecretSourceHostEnv},
				{Name: "", Source: SecretSource("bogus")},
			},
			wantErr: ErrEmptySecretName,
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
		refs     []SecretRef
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
			refs:     []SecretRef{},
			declared: declared,
		},
		{
			name: "valid env ref passes",
			refs: []SecretRef{
				{Name: "token", Env: "GITHUB_TOKEN"},
			},
			declared: declared,
		},
		{
			name: "valid mount ref passes",
			refs: []SecretRef{
				{Name: "cert", Mount: "/run/secrets/cert.pem"},
			},
			declared: declared,
		},
		{
			name: "multiple valid refs pass",
			refs: []SecretRef{
				{Name: "token", Env: "GH_TOKEN"},
				{Name: "cert", Mount: "/run/secrets/cert"},
			},
			declared: declared,
		},
		{
			name: "empty ref name returns ErrEmptySecretName",
			refs: []SecretRef{
				{Name: "", Env: "FOO"},
			},
			declared: declared,
			wantErr:  ErrEmptySecretName,
		},
		{
			name: "undeclared secret returns ErrUnknownSecret",
			refs: []SecretRef{
				{Name: "nonexistent", Env: "SECRET"},
			},
			declared: declared,
			wantErr:  ErrUnknownSecret,
		},
		{
			name: "duplicate ref returns ErrDuplicateSecretRef",
			refs: []SecretRef{
				{Name: "token", Env: "TOKEN_A"},
				{Name: "token", Env: "TOKEN_B"},
			},
			declared: declared,
			wantErr:  ErrDuplicateSecretRef,
		},
		{
			name: "no env or mount returns ErrSecretRefNoTarget",
			refs: []SecretRef{
				{Name: "token"},
			},
			declared: declared,
			wantErr:  ErrSecretRefNoTarget,
		},
		{
			name: "both env and mount returns ErrSecretRefBothTarget",
			refs: []SecretRef{
				{Name: "token", Env: "TOKEN", Mount: "/run/secrets/token"},
			},
			declared: declared,
			wantErr:  ErrSecretRefBothTarget,
		},
		{
			name: "relative mount path returns ErrRelativeSecretMount",
			refs: []SecretRef{
				{Name: "token", Mount: "run/secrets/token"},
			},
			declared: declared,
			wantErr:  ErrRelativeSecretMount,
		},
		{
			name: "invalid env name returns ErrInvalidSecretEnvName",
			refs: []SecretRef{
				{Name: "token", Env: "1INVALID"},
			},
			declared: declared,
			wantErr:  ErrInvalidSecretEnvName,
		},
		{
			name: "env name with hyphens returns ErrInvalidSecretEnvName",
			refs: []SecretRef{
				{Name: "token", Env: "MY-TOKEN"},
			},
			declared: declared,
			wantErr:  ErrInvalidSecretEnvName,
		},
		{
			name: "env name with dots returns ErrInvalidSecretEnvName",
			refs: []SecretRef{
				{Name: "token", Env: "my.token"},
			},
			declared: declared,
			wantErr:  ErrInvalidSecretEnvName,
		},
		{
			name: "underscore-prefixed env name passes",
			refs: []SecretRef{
				{Name: "token", Env: "_TOKEN_123"},
			},
			declared: declared,
		},
		{
			name: "duplicate env target returns ErrDuplicateSecretEnvName",
			refs: []SecretRef{
				{Name: "token", Env: "API_KEY"},
				{Name: "cert", Env: "API_KEY"},
			},
			declared: declared,
			wantErr:  ErrDuplicateSecretEnvName,
		},
		{
			name: "duplicate mount target returns ErrDuplicateSecretMount",
			refs: []SecretRef{
				{Name: "token", Mount: "/run/secrets/key"},
				{Name: "cert", Mount: "/run/secrets/key"},
			},
			declared: declared,
			wantErr:  ErrDuplicateSecretMount,
		},
		{
			name: "empty declared set rejects all refs",
			refs: []SecretRef{
				{Name: "token", Env: "TOKEN"},
			},
			declared: map[string]struct{}{},
			wantErr:  ErrUnknownSecret,
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
		jobRefs  []SecretRef
		stepRefs []SecretRef
		wantErr  error
	}{
		{
			name:     "no overlap passes",
			jobRefs:  []SecretRef{{Name: "token", Env: "TOKEN"}},
			stepRefs: []SecretRef{{Name: "cert", Mount: "/run/secrets/cert"}},
		},
		{
			name:     "nil job refs passes",
			jobRefs:  nil,
			stepRefs: []SecretRef{{Name: "token", Env: "TOKEN"}},
		},
		{
			name:     "nil step refs passes",
			jobRefs:  []SecretRef{{Name: "token", Env: "TOKEN"}},
			stepRefs: nil,
		},
		{
			name:     "both nil passes",
			jobRefs:  nil,
			stepRefs: nil,
		},
		{
			name:     "both empty passes",
			jobRefs:  []SecretRef{},
			stepRefs: []SecretRef{},
		},
		{
			name:     "step overriding job secret returns ErrSecretOverride",
			jobRefs:  []SecretRef{{Name: "token", Env: "TOKEN"}},
			stepRefs: []SecretRef{{Name: "token", Mount: "/run/secrets/token"}},
			wantErr:  ErrSecretOverride,
		},
		{
			name: "step overriding one of multiple job secrets returns ErrSecretOverride",
			jobRefs: []SecretRef{
				{Name: "token", Env: "TOKEN"},
				{Name: "cert", Mount: "/run/secrets/cert"},
			},
			stepRefs: []SecretRef{
				{Name: "cert", Env: "CERT_CONTENT"},
			},
			wantErr: ErrSecretOverride,
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

		decls := []SecretDecl{
			{Name: "token", Source: SecretSourceHostEnv},
			{Name: "cert", Source: SecretSourceFile, Path: "/etc/cert.pem"},
			{Name: "vault", Source: SecretSourceCmd, Cmd: "vault read"},
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

		got := secretDeclSet([]SecretDecl{})

		assert.Empty(t, got)
	})
}

// validJob returns a minimal valid job for use in pipeline validation tests.
// Callers override fields as needed to test specific secret behaviors.
func validJob(name string) Job {
	return Job{
		Name:  name,
		Image: "alpine:latest",
		Steps: []Step{{Name: "run", Run: []string{"echo ok"}}},
	}
}

func TestPipelineValidateSecrets(t *testing.T) {
	t.Parallel()

	t.Run("valid pipeline with secrets passes", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "gh-token", Env: "GITHUB_TOKEN"},
			{Name: "tls-cert", Mount: "/run/secrets/cert.pem"},
		}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "gh-token", Source: SecretSourceHostEnv, Var: "GH_TOKEN"},
				{Name: "tls-cert", Source: SecretSourceFile, Path: "/etc/ssl/cert.pem"},
				{Name: "db-pass", Source: SecretSourceCmd, Cmd: "vault kv get db/pass"},
			},
			Jobs: []Job{j},
		}

		// Act
		order, err := p.Validate()

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []int{0}, order)
	})

	t.Run("valid pipeline with step-level secrets passes", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "gh-token", Env: "GITHUB_TOKEN"},
		}
		j.Steps = []Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []SecretRef{{Name: "tls-cert", Mount: "/run/secrets/cert"}},
		}}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "gh-token", Source: SecretSourceHostEnv, Var: "GH_TOKEN"},
				{Name: "tls-cert", Source: SecretSourceFile, Path: "/etc/cert.pem"},
			},
			Jobs: []Job{j},
		}

		// Act
		order, err := p.Validate()

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []int{0}, order)
	})

	t.Run("unknown secret ref in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "nonexistent", Env: "SECRET"},
		}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "gh-token", Source: SecretSourceHostEnv, Var: "GH_TOKEN"},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrUnknownSecret)
	})

	t.Run("unknown secret ref in step fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Steps = []Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []SecretRef{{Name: "missing", Env: "SECRET"}},
		}}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "gh-token", Source: SecretSourceHostEnv, Var: "GH_TOKEN"},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrUnknownSecret)
	})

	t.Run("step overriding job secret fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "gh-token", Env: "GITHUB_TOKEN"},
		}
		j.Steps = []Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []SecretRef{{Name: "gh-token", Mount: "/run/secrets/token"}},
		}}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "gh-token", Source: SecretSourceHostEnv, Var: "GH_TOKEN"},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrSecretOverride)
	})

	t.Run("invalid pipeline-level secret decl fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("build")
		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "", Source: SecretSourceHostEnv},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrEmptySecretName)
	})

	t.Run("duplicate secret ref in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "token", Env: "TOKEN_A"},
			{Name: "token", Env: "TOKEN_B"},
		}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrDuplicateSecretRef)
	})

	t.Run("secret ref with no target in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "token"},
		}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrSecretRefNoTarget)
	})

	t.Run("secret ref with both targets in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "token", Env: "TOKEN", Mount: "/run/secrets/token"},
		}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrSecretRefBothTarget)
	})

	t.Run("relative mount path in job fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "token", Mount: "relative/path"},
		}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrRelativeSecretMount)
	})

	t.Run("invalid env name in job secret ref fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "token", Env: "1INVALID"},
		}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "token", Source: SecretSourceHostEnv, Var: "VALID_VAR"},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrInvalidSecretEnvName)
	})

	t.Run("step env colliding with job env target fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "token_a", Env: "TOKEN"},
		}
		j.Steps = []Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []SecretRef{{Name: "token_b", Env: "TOKEN"}},
		}}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "token_a", Source: SecretSourceHostEnv},
				{Name: "token_b", Source: SecretSourceHostEnv},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrDuplicateSecretEnvName)
	})

	t.Run("step mount colliding with job mount target fails", func(t *testing.T) {
		t.Parallel()

		// Arrange
		j := validJob("deploy")
		j.Secrets = []SecretRef{
			{Name: "cert_a", Mount: "/run/secrets/cert"},
		}
		j.Steps = []Step{{
			Name:    "push",
			Run:     []string{"deploy.sh"},
			Secrets: []SecretRef{{Name: "cert_b", Mount: "/run/secrets/cert"}},
		}}

		p := Pipeline{
			Name: "test",
			Secrets: []SecretDecl{
				{Name: "cert_a", Source: SecretSourceFile, Path: "/etc/cert_a.pem"},
				{Name: "cert_b", Source: SecretSourceFile, Path: "/etc/cert_b.pem"},
			},
			Jobs: []Job{j},
		}

		// Act
		_, err := p.Validate()

		// Assert
		require.ErrorIs(t, err, ErrDuplicateSecretMount)
	})

	t.Run("pipeline with no secrets and no refs passes", func(t *testing.T) {
		t.Parallel()

		// Arrange
		p := Pipeline{
			Name: "test",
			Jobs: []Job{validJob("build")},
		}

		// Act
		order, err := p.Validate()

		// Assert
		require.NoError(t, err)
		assert.Equal(t, []int{0}, order)
	})
}
