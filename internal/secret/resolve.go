// Package secret resolves pipeline secret declarations to concrete values
// on the host before BuildKit solve.
package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ndisidore/cicada/pkg/pipeline/pipelinemodel"
)

// _maxSecretFileSize caps file-sourced secrets to match BuildKit's
// secretsprovider.MaxSecretSize (500 * 1024).
const _maxSecretFileSize = 500 * 1024

// ResolvedSecret pairs a declaration name with its resolved byte value.
type ResolvedSecret struct {
	Name  string
	Value []byte
}

// Resolve resolves all secret declarations to concrete values. Host-side
// resolution happens before BuildKit solve so secrets never leave the host
// except through BuildKit's session gRPC.
func Resolve(ctx context.Context, decls []pipelinemodel.SecretDecl) ([]ResolvedSecret, error) {
	out := make([]ResolvedSecret, len(decls))
	for i, d := range decls {
		val, err := resolveOne(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("secret %q: %w: %w", d.Name, pipelinemodel.ErrSecretResolution, err)
		}
		out[i] = ResolvedSecret{Name: d.Name, Value: val}
	}
	return out, nil
}

// ToMap converts resolved secrets into a name->value map suitable for
// secretsprovider.FromMap.
func ToMap(secrets []ResolvedSecret) map[string][]byte {
	m := make(map[string][]byte, len(secrets))
	for _, s := range secrets {
		m[s.Name] = s.Value
	}
	return m
}

// PlaintextValues returns a name->string map of secret values for redaction.
// Empty values are excluded to avoid replacing empty strings in output.
func PlaintextValues(secrets []ResolvedSecret) map[string]string {
	m := make(map[string]string, len(secrets))
	for _, s := range secrets {
		v := string(s.Value)
		if v != "" {
			m[s.Name] = v
		}
	}
	return m
}

func resolveOne(ctx context.Context, d pipelinemodel.SecretDecl) ([]byte, error) {
	switch d.Source {
	case pipelinemodel.SecretSourceHostEnv:
		return resolveHostEnv(d)
	case pipelinemodel.SecretSourceFile:
		return resolveFile(d)
	case pipelinemodel.SecretSourceCmd:
		return resolveCmd(ctx, d)
	default:
		return nil, fmt.Errorf("unknown source %q", d.Source)
	}
}

func resolveHostEnv(d pipelinemodel.SecretDecl) ([]byte, error) {
	envName := d.Var
	if envName == "" {
		envName = d.Name
	}
	val, ok := os.LookupEnv(envName)
	if !ok {
		return nil, fmt.Errorf("environment variable %q not set", envName)
	}
	return []byte(val), nil
}

func resolveFile(d pipelinemodel.SecretDecl) ([]byte, error) {
	path := expandHome(d.Path)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening %q: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // Read-only; data already consumed before close.

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	if info.Size() > _maxSecretFileSize {
		return nil, fmt.Errorf("file %q is %d bytes (max %d)", path, info.Size(), _maxSecretFileSize)
	}

	// LimitReader caps actual I/O in case the file grows after Stat.
	data, err := io.ReadAll(io.LimitReader(f, _maxSecretFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", path, err)
	}
	if int64(len(data)) > _maxSecretFileSize {
		return nil, fmt.Errorf("file %q exceeds %d bytes", path, _maxSecretFileSize)
	}
	return data, nil
}

func resolveCmd(ctx context.Context, d pipelinemodel.SecretDecl) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", d.Cmd) //nolint:gosec // G204: Cmd comes from user's pipeline definition, not external input.
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("running %q: %w: %s", d.Cmd, err, bytes.TrimRight(exitErr.Stderr, "\n"))
		}
		return nil, fmt.Errorf("running %q: %w", d.Cmd, err)
	}
	return bytes.TrimRight(out, "\n"), nil
}

// expandHome replaces a leading "~/" or bare "~" with the user's home directory.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + path[1:]
}
