package filesync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashCache_RoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cache.json")
	c := NewHashCache(path)
	c.Update("a.go", 1000, 42, "abc123")
	c.Update("b.go", 2000, 99, "def456")

	require.NoError(t, c.Save())

	loaded := NewHashCache(path)
	require.NoError(t, loaded.Load())

	hash, ok := loaded.Lookup("a.go", 1000, 42)
	assert.True(t, ok)
	assert.Equal(t, "abc123", hash)

	hash, ok = loaded.Lookup("b.go", 2000, 99)
	assert.True(t, ok)
	assert.Equal(t, "def456", hash)
}

func TestHashCache_ColdStart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nonexistent", "cache.json")
	c := NewHashCache(path)

	require.NoError(t, c.Load())

	_, ok := c.Lookup("anything", 0, 0)
	assert.False(t, ok)
}

func TestHashCache_CorruptFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cache.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	c := NewHashCache(path)
	err := c.Load()

	require.ErrorIs(t, err, ErrCacheCorrupt)
}

func TestHashCache_VersionMismatch(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "cache.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":999,"entries":{}}`), 0o644))

	c := NewHashCache(path)
	err := c.Load()

	require.ErrorIs(t, err, ErrCacheCorrupt)
	assert.Contains(t, err.Error(), "version 999")
}

func TestHashCache_AtomicSave(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "cache.json")

	c := NewHashCache(path)
	c.Update("x.go", 100, 10, "aaa")
	require.NoError(t, c.Save())

	// Verify no temp files are left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "cache.json", entries[0].Name())

	// Verify content is valid after save.
	loaded := NewHashCache(path)
	require.NoError(t, loaded.Load())
	hash, ok := loaded.Lookup("x.go", 100, 10)
	assert.True(t, ok)
	assert.Equal(t, "aaa", hash)
}

func TestHashCache_LookupStale(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		mtime int64
		size  int64
	}{
		{"stale mtime", 9999, 42},
		{"stale size", 1000, 999},
		{"both stale", 9999, 999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := NewHashCache(filepath.Join(t.TempDir(), "cache.json"))
			c.Update("a.go", 1000, 42, "abc")

			_, ok := c.Lookup("a.go", tt.mtime, tt.size)
			assert.False(t, ok)
		})
	}
}

func TestCachePath_Deterministic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	p1, err := CachePath(dir)
	require.NoError(t, err)
	p2, err := CachePath(dir)
	require.NoError(t, err)
	assert.Equal(t, p1, p2, "same workdir should produce same path")

	other, err := CachePath(t.TempDir())
	require.NoError(t, err)
	assert.NotEqual(t, p1, other, "different workdirs should produce different paths")
}
