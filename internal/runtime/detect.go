//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime

import (
	"context"
	"fmt"
	"strings"
)

// Resolver finds and returns container runtimes.
// Factories are tried in order during auto-detection.
type Resolver struct {
	Factories []func() Runtime
}

// DefaultResolver returns a Resolver that tries Docker then Podman.
func DefaultResolver() *Resolver {
	return &Resolver{
		Factories: []func() Runtime{
			func() Runtime { return NewDocker() },
			func() Runtime { return NewPodman() },
		},
	}
}

// Detect returns the first available runtime from the factory list.
func (r *Resolver) Detect(ctx context.Context) (Runtime, error) {
	for _, factory := range r.Factories {
		rt := factory()
		if rt == nil {
			continue
		}
		if rt.Available(ctx) {
			return rt, nil
		}
	}
	return nil, ErrNoRuntime
}

// Get returns the named runtime, or auto-detects if name is "" or "auto".
// For explicit names, the runtime is returned without an availability check;
// errors surface naturally when runtime methods are called.
func (r *Resolver) Get(ctx context.Context, name string) (Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "auto":
		return r.Detect(ctx)
	case string(Docker):
		return NewDocker(), nil
	case string(Podman):
		return NewPodman(), nil
	default:
		return nil, fmt.Errorf("%q (valid: auto, docker, podman): %w", name, ErrUnknownRuntime)
	}
}
