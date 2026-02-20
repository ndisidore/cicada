package filesync

import (
	"context"
	"fmt"
	"io"
	gofs "io/fs"
	"log/slog"
	"sync/atomic"

	"github.com/tonistiigi/fsutil"
	"github.com/tonistiigi/fsutil/types"

	"github.com/ndisidore/cicada/internal/progress"
	"github.com/ndisidore/cicada/pkg/slogctx"
)

// Compile-time interface check.
var _ fsutil.FS = (*hashFS)(nil)

// Options configures the content-hash FS wrapper.
type Options struct {
	Cache  *HashCache      // persistent hash store; nil = no caching
	Sender progress.Sender // receives SyncMsg after Walk; nil = no reporting
}

// New wraps a base fsutil.FS with content-hash change detection and
// optional progress reporting. The wrapper is transparent to BuildKit:
// DirEntry metadata flows through unmodified.
func New(inner fsutil.FS, opts Options) fsutil.FS {
	return &hashFS{
		inner:  inner,
		cache:  opts.Cache,
		sender: opts.Sender,
	}
}

type hashFS struct {
	inner   fsutil.FS
	cache   *HashCache
	sender  progress.Sender
	metrics syncMetrics
}

// syncMetrics accumulates counters during Walk.
type syncMetrics struct {
	filesWalked atomic.Int64
	filesHashed atomic.Int64
	cacheHits   atomic.Int64
}

// Walk delegates to the inner FS, computing content hashes for regular files
// and reporting sync metrics on completion.
func (fs *hashFS) Walk(ctx context.Context, target string, fn gofs.WalkDirFunc) error {
	fs.metrics.filesWalked.Store(0)
	fs.metrics.filesHashed.Store(0)
	fs.metrics.cacheHits.Store(0)

	if fs.cache != nil {
		if err := fs.cache.Load(); err != nil {
			slogctx.FromContext(ctx).LogAttrs(
				ctx,
				slog.LevelDebug,
				"loading sync cache",
				slog.Any("error", err),
			)
		}
	}

	seen := make(map[string]struct{})
	err := fs.inner.Walk(ctx, target, func(path string, d gofs.DirEntry, walkErr error) error {
		if fnErr := fn(path, d, walkErr); fnErr != nil {
			return fnErr
		}

		if walkErr == nil && d != nil && d.Type().IsRegular() {
			fs.metrics.filesWalked.Add(1)
			seen[path] = struct{}{}
			fs.hashEntry(ctx, path, d)
		}
		return nil
	})

	if fs.cache != nil {
		if err == nil {
			fs.cache.Prune(seen)
		}
		if saveErr := fs.cache.Save(); saveErr != nil {
			slogctx.FromContext(ctx).LogAttrs(
				ctx,
				slog.LevelDebug,
				"saving sync cache",
				slog.Any("error", saveErr),
			)
		}
	}

	if fs.sender != nil && err == nil {
		fs.sender.Send(progress.SyncMsg{
			FilesWalked: fs.metrics.filesWalked.Load(),
			FilesHashed: fs.metrics.filesHashed.Load(),
			CacheHits:   fs.metrics.cacheHits.Load(),
		})
	}

	return err
}

// hashEntry checks the cache for a regular file and computes SHA256 on miss.
func (fs *hashFS) hashEntry(ctx context.Context, path string, d gofs.DirEntry) {
	if fs.cache == nil {
		return
	}

	fi, err := d.Info()
	if err != nil {
		slogctx.FromContext(ctx).LogAttrs(
			ctx,
			slog.LevelDebug,
			"reading file info for hash",
			slog.String("path", path),
			slog.Any("error", err),
		)
		return
	}
	stat, ok := fi.Sys().(*types.Stat)
	if !ok {
		slogctx.FromContext(ctx).LogAttrs(
			ctx,
			slog.LevelDebug,
			"unexpected Sys() type for hash",
			slog.String("path", path),
			slog.String("type", fmt.Sprintf("%T", fi.Sys())),
		)
		return
	}

	if _, hit := fs.cache.Lookup(path, stat.GetModTime(), stat.GetSize()); hit {
		fs.metrics.cacheHits.Add(1)
		return
	}

	// Cache miss: compute content hash.
	rc, err := fs.inner.Open(path)
	if err != nil {
		slogctx.FromContext(ctx).LogAttrs(
			ctx,
			slog.LevelDebug,
			"opening file for hash",
			slog.String("path", path),
			slog.Any("error", err),
		)
		return
	}
	defer func() { _ = rc.Close() }()

	hash, err := computeSHA256(rc)
	if err != nil {
		slogctx.FromContext(ctx).LogAttrs(
			ctx,
			slog.LevelDebug,
			"hashing file",
			slog.String("path", path),
			slog.Any("error", err),
		)
		return
	}

	fs.cache.Update(path, stat.GetModTime(), stat.GetSize(), hash)
	fs.metrics.filesHashed.Add(1)
}

// Open delegates to the inner FS.
func (fs *hashFS) Open(p string) (io.ReadCloser, error) {
	rc, err := fs.inner.Open(p)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", p, err)
	}
	return rc, nil
}
