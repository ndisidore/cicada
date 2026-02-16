//revive:disable:var-naming Package name conflict with standard library is intentional.
package runtime

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Resolver finds and returns container runtimes.
// Factories are tried in order during auto-detection.
// Named maps explicit runtime names to constructors for Get.
type Resolver struct {
	Factories []func() Runtime
	Named     map[Type]func() Runtime
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
	key := strings.ToLower(strings.TrimSpace(name))
	switch key {
	case "", "auto":
		return r.Detect(ctx)
	default:
		if r.Named == nil {
			return nil, fmt.Errorf("%q (resolver has no named runtimes): %w", name, ErrUnknownRuntime)
		}
		factory, ok := r.Named[Type(key)]
		if !ok {
			valid := make([]string, 0, len(r.Named)+1)
			valid = append(valid, "auto")
			for k := range r.Named {
				valid = append(valid, string(k))
			}
			slices.Sort(valid)
			return nil, fmt.Errorf("%q (valid: %s): %w", name, strings.Join(valid, ", "), ErrUnknownRuntime)
		}
		rt := factory()
		if rt == nil {
			return nil, fmt.Errorf("%q: %w", name, ErrNilFactory)
		}
		return rt, nil
	}
}
