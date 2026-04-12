// Package cache provides cache spec parsing and analytics for BuildKit cache
// import/export operations.
package cache

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/moby/buildkit/client"
)

// Sentinel errors for cache spec parsing.
var (
	ErrInvalidCacheSpec = errors.New("invalid cache spec")
	ErrUnknownCacheType = errors.New("unknown cache type")
)

// _knownTypes lists the cache backend types BuildKit supports.
var _knownTypes = map[string]struct{}{
	"registry": {},
	"gha":      {},
	"local":    {},
	"inline":   {},
	"s3":       {},
}

// ParseSpec parses a single cache spec string into a CacheOptionsEntry.
// Format: type=registry,ref=ghcr.io/user/cache,mode=max
// Shorthand: if no '=' is present, treats the string as type=registry,ref=<raw>.
func ParseSpec(raw string) (client.CacheOptionsEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return client.CacheOptionsEntry{}, fmt.Errorf("%w: empty spec", ErrInvalidCacheSpec)
	}

	// Shorthand: bare string without '=' is a registry ref.
	if !strings.Contains(raw, "=") {
		return client.CacheOptionsEntry{
			Type: "registry",
			Attrs: map[string]string{
				"ref": raw,
			},
		}, nil
	}

	attrs := make(map[string]string)
	for part := range strings.SplitSeq(raw, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || k == "" {
			return client.CacheOptionsEntry{}, fmt.Errorf("%w: malformed key-value %q", ErrInvalidCacheSpec, part)
		}
		attrs[k] = v
	}

	cacheType, ok := attrs["type"]
	if !ok {
		return client.CacheOptionsEntry{}, fmt.Errorf("%w: missing type attribute", ErrInvalidCacheSpec)
	}
	delete(attrs, "type")

	if _, known := _knownTypes[cacheType]; !known {
		return client.CacheOptionsEntry{}, fmt.Errorf("%w: %q", ErrUnknownCacheType, cacheType)
	}

	return client.CacheOptionsEntry{
		Type:  cacheType,
		Attrs: attrs,
	}, nil
}

// ParseSpecs parses multiple cache spec strings.
func ParseSpecs(raw []string) ([]client.CacheOptionsEntry, error) {
	entries := make([]client.CacheOptionsEntry, 0, len(raw))
	for _, r := range raw {
		e, err := ParseSpec(r)
		if err != nil {
			return nil, fmt.Errorf("spec %q: %w", r, err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// DetectGHA auto-populates url, url_v2, and token attributes for type=gha
// entries from well-known GitHub Actions environment variables. Entries that
// already carry explicit values for those attributes are left unchanged.
//
// Supported env vars:
//   - ACTIONS_RESULTS_URL  → url_v2 (cache v2 / Actions Results API)
//   - ACTIONS_CACHE_URL    → url    (cache v1 API, older runners)
//   - ACTIONS_RUNTIME_TOKEN → token (auth for both APIs)
func DetectGHA(entries []client.CacheOptionsEntry) []client.CacheOptionsEntry {
	urlV2 := os.Getenv("ACTIONS_RESULTS_URL")
	urlV1 := os.Getenv("ACTIONS_CACHE_URL")
	token := os.Getenv("ACTIONS_RUNTIME_TOKEN")

	for i := range entries {
		if entries[i].Type != "gha" {
			continue
		}
		if entries[i].Attrs == nil {
			entries[i].Attrs = make(map[string]string)
		}
		if _, ok := entries[i].Attrs["url_v2"]; !ok && urlV2 != "" {
			entries[i].Attrs["url_v2"] = urlV2
		}
		if _, ok := entries[i].Attrs["url"]; !ok && urlV1 != "" {
			entries[i].Attrs["url"] = urlV1
		}
		if _, ok := entries[i].Attrs["token"]; !ok && token != "" {
			entries[i].Attrs["token"] = token
		}
	}
	return entries
}
