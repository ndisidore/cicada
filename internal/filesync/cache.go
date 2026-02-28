// Package filesync provides a content-hash-aware fsutil.FS wrapper with
// persistent caching and progress reporting for BuildKit file sync.
package filesync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
)

// ErrCacheCorrupt indicates the on-disk cache file could not be parsed.
var ErrCacheCorrupt = errors.New("hash cache corrupt")

// _cacheVersion is the current on-disk cache format version.
const _cacheVersion = 1

// HashCache stores per-file content hashes keyed by (path, mtime, size).
type HashCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	path    string
}

type cacheEntry struct {
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime"`
	Hash    string `json:"hash"`
}

// cacheFile is the on-disk JSON envelope.
type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

// CachePath derives the per-project cache file path under os.UserCacheDir.
// The filename is the hex SHA256 of the absolute workdir path, ensuring
// distinct projects never collide.
func CachePath(workdir string) (string, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolving absolute path for %s: %w", workdir, err)
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolving user cache dir: %w", err)
	}
	h := sha256.Sum256([]byte(abs))
	name := hex.EncodeToString(h[:]) + ".json"
	return filepath.Join(base, "cicada", "sync", name), nil
}

// NewHashCache returns a HashCache that persists at filePath.
func NewHashCache(filePath string) *HashCache {
	return &HashCache{
		entries: make(map[string]cacheEntry),
		path:    filePath,
	}
}

// Load reads the cache from disk. A missing file is not an error (cold start).
// Malformed JSON or a version mismatch returns ErrCacheCorrupt.
func (c *HashCache) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("reading cache %s: %w", c.path, err)
	}

	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("%w: %w", ErrCacheCorrupt, err)
	}
	if f.Version != _cacheVersion {
		return fmt.Errorf("%w: version %d (expected %d)", ErrCacheCorrupt, f.Version, _cacheVersion)
	}

	if f.Entries == nil {
		c.entries = make(map[string]cacheEntry)
	} else {
		c.entries = f.Entries
	}
	return nil
}

// Save atomically writes the cache to disk via temp file + rename.
// The lock is held only long enough to snapshot the entries map, so
// Lookup/Update are not blocked during disk I/O.
func (c *HashCache) Save() error {
	c.mu.RLock()
	snapshot := make(map[string]cacheEntry, len(c.entries))
	maps.Copy(snapshot, c.entries)
	c.mu.RUnlock()

	data, err := json.Marshal(cacheFile{
		Version: _cacheVersion,
		Entries: snapshot,
	})
	if err != nil {
		return fmt.Errorf("marshaling cache: %w", err)
	}

	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating cache dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".cicada-sync-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp cache file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName) //nolint:gosec // G703: path comes from os.CreateTemp, not user input
		return fmt.Errorf("writing temp cache file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName) //nolint:gosec // G703: path comes from os.CreateTemp, not user input
		return fmt.Errorf("syncing temp cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName) //nolint:gosec // G703: path comes from os.CreateTemp, not user input
		return fmt.Errorf("closing temp cache file: %w", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil { //nolint:gosec // G703: path comes from os.CreateTemp, not user input
		_ = os.Remove(tmpName) //nolint:gosec // G703: path comes from os.CreateTemp, not user input
		return fmt.Errorf("renaming cache file: %w", err)
	}
	return nil
}

// Lookup returns the cached entry for path if mtime and size still match.
func (c *HashCache) Lookup(path string, mtime, size int64) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	e, ok := c.entries[path]
	if !ok || e.ModTime != mtime || e.Size != size {
		return "", false
	}
	return e.Hash, true
}

// Update stores a hash entry for the given path.
func (c *HashCache) Update(path string, mtime, size int64, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[path] = cacheEntry{
		Size:    size,
		ModTime: mtime,
		Hash:    hash,
	}
}

// Prune removes cache entries for paths not present in the seen set.
func (c *HashCache) Prune(seen map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for path := range c.entries {
		if _, ok := seen[path]; !ok {
			delete(c.entries, path)
		}
	}
}

// computeSHA256 returns the hex-encoded SHA256 digest of r.
func computeSHA256(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("computing SHA256: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
