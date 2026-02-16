// Package gitinfo detects git branch and tag from CI environment variables
// or local git state.
package gitinfo

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/ndisidore/cicada/pkg/slogctx"
)

// _gitTimeout is the maximum duration for local git subprocesses.
const _gitTimeout = 5 * time.Second

// DetectBranch returns the current git branch from CI environment variables
// or local git state. Returns empty string if detection fails. The getenv
// parameter controls environment variable lookups (typically os.Getenv).
//
//revive:disable-next-line:cognitive-complexity DetectBranch is a linear chain of CI provider fallbacks; splitting it hurts readability.
func DetectBranch(ctx context.Context, getenv func(string) string) string {
	// CI environment variables, checked in priority order.
	if v := getenv("GITHUB_HEAD_REF"); v != "" {
		return v
	}
	if ref := getenv("GITHUB_REF_NAME"); ref != "" {
		if getenv("GITHUB_REF_TYPE") != "tag" {
			return ref
		}
	}
	if v := getenv("CI_COMMIT_BRANCH"); v != "" {
		return v
	}
	if v := getenv("CIRCLE_BRANCH"); v != "" {
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelDebug, "git branch detection skipped", slog.String("error", err.Error()))
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "HEAD" {
		return ""
	}
	return branch
}

// DetectTag returns the git tag pointing at HEAD from CI environment
// variables or local git state. Returns empty string if no tag is found.
// The getenv parameter controls environment variable lookups (typically
// os.Getenv).
func DetectTag(ctx context.Context, getenv func(string) string) string {
	if ref := getenv("GITHUB_REF_NAME"); ref != "" {
		if getenv("GITHUB_REF_TYPE") == "tag" {
			return ref
		}
	}
	if v := getenv("CI_COMMIT_TAG"); v != "" {
		return v
	}
	if v := getenv("CIRCLE_TAG"); v != "" {
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, _gitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "describe", "--exact-match", "--tags", "HEAD").Output()
	if err != nil {
		slogctx.FromContext(ctx).LogAttrs(ctx, slog.LevelDebug, "git tag detection skipped", slog.String("error", err.Error()))
		return ""
	}
	return strings.TrimSpace(string(out))
}
