package filesync

import (
	"context"
	gofs "io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
	"github.com/tonistiigi/fsutil/types"

	"github.com/ndisidore/cicada/internal/progress/progressmodel"
)

// captureSender captures the last SyncMsg sent.
type captureSender struct {
	mu  sync.Mutex
	msg *progressmodel.SyncMsg
}

func (s *captureSender) Send(m progressmodel.Msg) {
	if sm, ok := m.(progressmodel.SyncMsg); ok {
		s.mu.Lock()
		s.msg = &sm
		s.mu.Unlock()
	}
}

func (s *captureSender) get() *progressmodel.SyncMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.msg
}

// setupTestDir creates a temp directory with test files and returns the base fsutil.FS.
func setupTestDir(t *testing.T, files map[string]string) (string, fsutil.FS) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	base, err := fsutil.NewFS(dir)
	require.NoError(t, err)
	return dir, base
}

func TestHashFS_WalkForwardsTypeStat(t *testing.T) {
	t.Parallel()

	_, base := setupTestDir(t, map[string]string{"a.txt": "hello"})
	fs := New(base, Options{})

	var infos []gofs.FileInfo
	err := fs.Walk(t.Context(), "", func(path string, d gofs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		infos = append(infos, fi)
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, infos)

	for _, fi := range infos {
		stat, ok := fi.Sys().(*types.Stat)
		assert.True(t, ok, "Info().Sys() should return *types.Stat, got %T", fi.Sys())
		assert.NotNil(t, stat)
	}
}

func TestHashFS_CacheHitMiss(t *testing.T) {
	t.Parallel()

	dir, base := setupTestDir(t, map[string]string{
		"a.txt": "hello",
		"b.txt": "world",
	})

	cachePath := filepath.Join(t.TempDir(), "cache.json")
	sender := &captureSender{}

	// First walk: all misses.
	cache1 := NewHashCache(cachePath)
	fs1 := New(base, Options{Cache: cache1, Sender: sender})
	err := fs1.Walk(t.Context(), "", func(_ string, _ gofs.DirEntry, walkErr error) error {
		return walkErr
	})
	require.NoError(t, err)

	msg1 := sender.get()
	require.NotNil(t, msg1)
	assert.Equal(t, int64(2), msg1.FilesWalked)
	assert.Equal(t, int64(2), msg1.FilesHashed)
	assert.Equal(t, int64(0), msg1.CacheHits)

	// Second walk (unchanged): all hits.
	base2, err := fsutil.NewFS(dir)
	require.NoError(t, err)
	cache2 := NewHashCache(cachePath)
	fs2 := New(base2, Options{Cache: cache2, Sender: sender})
	err = fs2.Walk(t.Context(), "", func(_ string, _ gofs.DirEntry, walkErr error) error {
		return walkErr
	})
	require.NoError(t, err)

	msg2 := sender.get()
	require.NotNil(t, msg2)
	assert.Equal(t, int64(2), msg2.FilesWalked)
	assert.Equal(t, int64(0), msg2.FilesHashed)
	assert.Equal(t, int64(2), msg2.CacheHits)

	// Modify one file, third walk: 1 miss + 1 hit.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed!"), 0o644))
	base3, err := fsutil.NewFS(dir)
	require.NoError(t, err)
	cache3 := NewHashCache(cachePath)
	fs3 := New(base3, Options{Cache: cache3, Sender: sender})
	err = fs3.Walk(t.Context(), "", func(_ string, _ gofs.DirEntry, walkErr error) error {
		return walkErr
	})
	require.NoError(t, err)

	msg3 := sender.get()
	require.NotNil(t, msg3)
	assert.Equal(t, int64(2), msg3.FilesWalked)
	assert.Equal(t, int64(1), msg3.FilesHashed)
	assert.Equal(t, int64(1), msg3.CacheHits)
}

func TestHashFS_NilSender(t *testing.T) {
	t.Parallel()

	_, base := setupTestDir(t, map[string]string{"x.txt": "data"})
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	hfs := New(base, Options{Cache: NewHashCache(cachePath)})

	err := hfs.Walk(t.Context(), "", func(_ string, _ gofs.DirEntry, walkErr error) error {
		return walkErr
	})
	require.NoError(t, err)
}

func TestHashFS_NilCache(t *testing.T) {
	t.Parallel()

	_, base := setupTestDir(t, map[string]string{"x.txt": "data"})
	sender := &captureSender{}
	hfs := New(base, Options{Sender: sender})

	err := hfs.Walk(t.Context(), "", func(_ string, _ gofs.DirEntry, walkErr error) error {
		return walkErr
	})
	require.NoError(t, err)

	msg := sender.get()
	require.NotNil(t, msg)
	assert.Equal(t, int64(1), msg.FilesWalked)
	assert.Equal(t, int64(0), msg.FilesHashed, "no cache means no hashing, so no changes detected")
	assert.Equal(t, int64(0), msg.CacheHits)
}

func TestHashFS_WalkContextCancel(t *testing.T) {
	t.Parallel()

	t.Run("no cache", func(t *testing.T) {
		t.Parallel()

		_, base := setupTestDir(t, map[string]string{
			"a.txt": "one",
			"b.txt": "two",
			"c.txt": "three",
		})
		hfs := New(base, Options{})

		ctx, cancel := context.WithCancel(t.Context())
		cancel() // cancel before walk

		err := hfs.Walk(ctx, "", func(_ string, _ gofs.DirEntry, walkErr error) error {
			return walkErr
		})
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("cache preserved on cancel", func(t *testing.T) {
		t.Parallel()

		dir, base := setupTestDir(t, map[string]string{
			"a.txt": "one",
			"b.txt": "two",
		})

		cachePath := filepath.Join(t.TempDir(), "cache.json")

		// Seed the cache with a successful walk.
		cache1 := NewHashCache(cachePath)
		fs1 := New(base, Options{Cache: cache1})
		require.NoError(t, fs1.Walk(t.Context(), "", func(_ string, _ gofs.DirEntry, walkErr error) error {
			return walkErr
		}))

		// Cancel context before walk — Prune must be skipped.
		base2, err := fsutil.NewFS(dir)
		require.NoError(t, err)
		cache2 := NewHashCache(cachePath)
		fs2 := New(base2, Options{Cache: cache2})

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err = fs2.Walk(ctx, "", func(_ string, _ gofs.DirEntry, walkErr error) error {
			return walkErr
		})
		require.ErrorIs(t, err, context.Canceled)

		// Reload cache and verify entries were preserved (not pruned).
		cache3 := NewHashCache(cachePath)
		require.NoError(t, cache3.Load())
		cache3.mu.RLock()
		_, existsA := cache3.entries["a.txt"]
		_, existsB := cache3.entries["b.txt"]
		cache3.mu.RUnlock()
		assert.True(t, existsA, "a.txt should still be in cache after cancelled walk")
		assert.True(t, existsB, "b.txt should still be in cache after cancelled walk")
	})
}
