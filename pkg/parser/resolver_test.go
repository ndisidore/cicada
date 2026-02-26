//revive:disable:var-naming Package name conflict with standard library is intentional.
package parser

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStdinHelpers(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		input       string
		expectedDir string
		expectedNam string
	}{
		{"stdin filename", _stdinFilename, ".", "stdin"},
		{"synthetic filename", _syntheticFilename, ".", ""},
		{"regular filename", "/home/user/ci.kdl", "/home/user", "ci"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expectedDir, dirOf(tc.input))
			assert.Equal(t, tc.expectedNam, nameFromFilename(tc.input))
		})
	}
}

func TestStdinResolver(t *testing.T) {
	t.Parallel()

	inner := &memResolver{files: map[string]string{
		"/abs/pipeline.kdl": `step "build" { run "echo ok" }`,
	}}
	content := `step "test" { run "echo stdin" }`

	t.Run("resolve", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name         string
			source       string
			wantResolved string
			wantBody     string
		}{
			{"dash reads from stdin", "-", _stdinFilename, content},
			{"non-dash delegates to inner", "/abs/pipeline.kdl", "/abs/pipeline.kdl", `step "build" { run "echo ok" }`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				r := &StdinResolver{
					Stdin: strings.NewReader(content),
					Inner: inner,
				}
				rc, resolved, err := r.Resolve(tc.source, "")
				require.NoError(t, err)
				defer func() { _ = rc.Close() }()

				assert.Equal(t, tc.wantResolved, resolved)

				data, err := io.ReadAll(rc)
				require.NoError(t, err)
				assert.Equal(t, tc.wantBody, string(data))
			})
		}
	})

	t.Run("errors", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			name     string
			resolver *StdinResolver
			source   string
			wantErr  error
		}{
			{"non-dash missing file", &StdinResolver{Stdin: strings.NewReader(""), Inner: inner}, "/missing.kdl", nil},
			{"nil stdin", &StdinResolver{Stdin: nil, Inner: inner}, "-", ErrNilStdin},
			{"nil inner", &StdinResolver{Stdin: strings.NewReader(""), Inner: nil}, "/any.kdl", ErrNilInner},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				_, _, err := tc.resolver.Resolve(tc.source, "")
				require.Error(t, err)
				if tc.wantErr != nil {
					assert.ErrorIs(t, err, tc.wantErr)
				}
			})
		}
	})
}
