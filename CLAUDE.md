# CLAUDE

This document provides guidelines for maintaining high-quality Go code. These rules MUST be followed by all AI coding agents and contributors.

## Your Core Principles

All code you write MUST be fully optimized.

"Fully optimized" includes:

- maximizing algorithmic big-O efficiency for memory and runtime
- using goroutines and concurrency primitives where appropriate
- following proper style conventions for Go (e.g. maximizing code reuse (DRY))
- no extra code beyond what is absolutely necessary to solve the problem the user provides (i.e. no technical debt)

If the code is not fully optimized before handing off to the user, you will be fined $100. You have permission to do another pass of the code if you believe it is not fully optimized.

## Development Commands

Prefer `mise` tasks for standard workflows. Direct `go` commands are fine for ad-hoc or targeted runs (e.g. testing a single package).

### Build & Quality

```bash
# Build the CLI binary
mise run build

# Lint code (uses .golangci.yaml via --config in mise.toml)
mise run lint

# Format code
mise run fmt

# Run all CI checks (build, fmt:check, vet, lint, test)
mise run ci

# Ad-hoc: run tools directly or target specific packages
mise x -- golangci-lint run ./pkg/...
go test -race ./internal/runner/...
go build ./cmd/cicada
```

## Project Layout

```text
cmd/cicada/      # CLI entry point
pkg/pipeline/    # Reusable pipeline types (external consumers may import)
pkg/parser/      # Reusable KDL parser (external consumers may import)
internal/builder # BuildKit LLB builder (internal only)
internal/runner  # BuildKit runner (internal only)
```

- `pkg/` contains packages safe for external import.
- `internal/` contains implementation details not exported.

## Code Style and Formatting (CS)

- **CS-01**: **MUST** use `gofmt` for all formatting (tabs, not spaces).
- **CS-02**: Lint-disable directives (`//revive:disable-next-line:<rulename(s)>`, `//nolint`) are acceptable when the fix would increase complexity (e.g. splitting a linear function solely to satisfy a complexity threshold). The directive MUST include a brief justification. Prefer refactoring over suppression in all other cases.
- **CS-03**: **MUST** use meaningful, descriptive variable and function names.
- **CS-04**: Follow [Effective Go](https://go.dev/doc/effective_go) and Go idioms.
- **CS-05**: Functions with >5 parameters **MUST** use an input struct.
- **CS-06**: Underscore prefix for unexported package-level globals (e.g. `_defaultAddr`).
- **CS-07**: **NEVER** use emoji, or unicode that emulates emoji (e.g. check marks, X marks). The only exception is when writing tests and testing the impact of multibyte characters.
- **CS-08 (SHOULD)**: Prefer generics to eliminate duplicated logic across types; avoid when concrete types or interfaces are clearer.

## Logging & Observability (OBS)

- **OBS-01 (MUST)** Structured logging (`slog`) with levels and consistent fields.
- **OBS-02 (SHOULD)** Correlate logs/metrics/traces via request IDs from context.
- **OBS-03 (MUST)** Comments within code should be concise point-in-time snapshots of **how** things presently work. There should be no meta-commentary about **what** changed.
- See **SEC-2** for sensitive information logging rules.

## Error Handling (ERR)

- **ERR-01**: All errors **MUST** be wrapped with `fmt.Errorf("context: %w", err)`.
- **ERR-02**: Use `errors.Is` and `errors.As` for error checking.
- **ERR-03**: Use sentinel errors (`var ErrFoo = errors.New(...)`) for expected failure modes.
- **ERR-04 (MUST)**: **NEVER** return `nil, nil`; use sentinel errors (`var ErrFoo = errors.New(...)`) for expected failure modes.
- **ERR-05 (MUST)** Handle errors only once: either log OR return (wrapped), never both.
- **ERR-06 (MUST)** Don't use "failed" or "error" in error wrapping - reserve for error origin.

## Testing (T)

- **T-01**: **SHOULD** use table-driven tests with `t.Parallel()`. Where table-driven tests are not possible, use nested `t.Run`s (up to a depth of 4) grouped by concern.
- **T-02**: **MUST** use `-race` flag when running tests.
- **T-03 (SHOULD)** Avoid shared state between tests to enable parallel/race running.
- **T-04 (SHOULD)**: Use [testing/synctest](https://pkg.go.dev/testing/synctest) when testing timing/concurrent code. Note: real network I/O requires fake implementations, and mutex-based synchronization has limitations.
- **MUST** mock external dependencies (APIs, databases, file systems).
- Follow the Arrange-Act-Assert pattern.
- Do not commit commented-out tests.

## Concurrency (CC)

- **CC-01**: The sender closes the channel.
- **CC-02**: Use `golang.org/x/sync/errgroup` for concurrent goroutine management.
- Prefer channels for communication, mutexes for state protection.
- Always use `context.Context` for cancellation.

## Dependencies

- **MD-1**: Only add external dependencies when there is clear payoff over hand-rolling.
- **MUST** run `go mod tidy` after dependency changes.
- Prefer stdlib where feasible.

## Security

- **SEC-1 (MUST)** Validate inputs; set explicit I/O timeouts; prefer TLS everywhere.
- **SEC-2 (MUST)** Never log secrets (passwords, tokens, PII); manage secrets outside code (env/secret manager).
- **SEC-3 (SHOULD)** Limit filesystem/network access by default; principle of least privilege.

## Version Control

- **MUST** write clear, descriptive commit messages.
- **NEVER** commit commented-out code; delete it.
- **NEVER** commit debug `fmt.Println` or `log.Println` statements.
- **NEVER** commit credentials or sensitive data.

## Tools

- **MUST** use `gofmt` for code formatting.
- **MUST** use `golangci-lint` for linting (prefer `mise run lint`).
- **MUST** ensure code compiles with no warnings.
- Prefer `mise run build` for full builds; `go build ./...` for ad-hoc targets.
- Prefer `mise run test` for full test suite; `go test -race ./...` for ad-hoc targets.
- Use `go vet ./...` for static analysis.
- Use `mise` for task automation.

---

**Remember:** Prioritize clarity, readability and maintainability over cleverness.
