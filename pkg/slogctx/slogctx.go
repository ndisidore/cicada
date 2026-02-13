// Package slogctx provides context-based slog logger injection.
package slogctx

import (
	"context"
	"log/slog"
)

type _ctxKey struct{}

// ContextWithLogger returns a new context carrying the given logger.
func ContextWithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, _ctxKey{}, logger)
}

// FromContext returns the logger stored in ctx, or slog.Default() if none.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(_ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
