# Cicada. Hear your CI before it ships.

<p align="center">
  <a href="https://github.com/ndisidore/cicada/actions/workflows/ci.yaml"><img src="https://github.com/ndisidore/cicada/actions/workflows/ci.yaml/badge.svg" alt="CI"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go"></a>
  <a href="https://github.com/moby/buildkit"><img src="https://img.shields.io/badge/BuildKit-powered-blue?logo=docker&logoColor=white" alt="BuildKit"></a>
  <a href="https://kdl.dev"><img src="https://img.shields.io/badge/config-KDL-blueviolet" alt="KDL"></a>
</p>

A container-native CI pipeline runner powered by [BuildKit](https://github.com/moby/buildkit).
Define pipelines in [KDL](https://kdl.dev), run them anywhere BuildKit runs -- your laptop included.

No more "push and pray."

## Motivation

Most CI systems share the same dirty secret: the only way to find out if your pipeline works is to push it and wait. You tweak some YAML, open a PR, stare at a spinner for 8 minutes, watch it fail on line 3, and do it all over again. It's the worst feedback loop in software engineering, and we've somehow normalized it.

Good CI should be better than that. Specifically, it should be:

- **Locally repeatable**: The exact same pipeline that runs in CI should run on your machine. Not a "close enough" approximation. The *same* thing. If it passes on your laptop, it passes in CI. Full stop.

- **Debuggable**: When something breaks, you should be able to drop into a shell, poke around, and iterate -- not squint at truncated logs from a VM you'll never touch.

- **Tool-centric**: CI should be a thin wrapper around your existing scripts and task runners, not a vendor-specific reimagination of how builds work. Your `mise` tasks, your Makefile, your shell scripts -- those are the source of truth.

- **Portable**: Switching CI providers shouldn't require rewriting your entire pipeline from scratch. Your build logic lives in *your* repo, not in some provider's proprietary DSL.

- **Fast via caching**: Content-hash-based caching should prevent redundant work. You shouldn't reinstall your dependencies every single run because the CI system forgot what happened 5 minutes ago.

Cicada takes these ideas seriously. Pipelines are declared in KDL (not YAML -- you're welcome), executed inside containers via BuildKit, and run the same way everywhere. Your laptop is a first-class CI environment.

## Comparison to Dagger

[Dagger](https://dagger.io/) is a major inspiration for Cicada. It proved that BuildKit is a fantastic execution engine for CI and that local-first pipelines are not just possible but *preferable*. Cicada wouldn't exist without the trail Dagger blazed.

That said, Dagger's power comes with friction that Cicada tries to avoid:

| | Dagger | Cicada |
|---|---|---|
| **Pipeline definition** | Go / TypeScript / Python SDK | KDL config file |
| **Learning curve** | SDK APIs, generated clients, GraphQL engine internals | One config format, handful of options |
| **Upgrade path** | Regenerate `./internal/dagger`, SDK version coupling, potential Go version mismatches | `go install` / update a binary |
| **API surface** | Large, partially chainable, partially not | Deliberately small -- steps, mounts, caches, dependencies |
| **Module ecosystem** | Daggerverse (powerful, but unclear trust/security model for secrets and env access) | None yet -- your pipeline is self-contained |
| **Runtime overhead** | GraphQL engine + SDK runtime + BuildKit | BuildKit (that's it) |

The short version: Dagger gives you a full programming language and an ecosystem to go with it. Cicada gives you a config file and gets out of the way. If your pipeline needs loops, conditionals, and dynamic graph construction, Dagger is the better tool. If your pipeline is "run these commands in these containers in this order," Cicada is the lighter path to get there.

Both agree on the thing that matters most: CI should run on your laptop.

## Prerequisites

- [mise](https://mise.jdx.dev/) -- task runner and tool manager (handles Go + linter versions for you)
- A running [BuildKit](https://github.com/moby/buildkit) daemon (`buildkitd`)

## Quick Start

```bash
# Install tools (Go, golangci-lint) via mise
mise install

# Build cicada
mise build

# Validate a pipeline
./bin/cicada validate examples/hello.kdl

# Run a pipeline
./bin/cicada run examples/hello.kdl
```

## Pipeline Syntax

Pipelines are written in [KDL](https://kdl.dev), a document language that's cleaner than YAML and more readable than JSON.

```kdl
pipeline "hello" {
  step "greet" {
    image "alpine:latest"
    run "echo 'Hello from Cicada!'"
  }

  step "build" {
    image "rust:1.76"
    depends-on "greet"
    mount "." "/src"
    workdir "/src"
    run "cargo build"
  }
}
```

### Step Options

| Option       | Description                              | Example                                  |
|--------------|------------------------------------------|------------------------------------------|
| `image`      | Container image to run in                | `image "golang:1.25"`                    |
| `run`        | Shell command (multiple allowed)         | `run "go test ./..."`                    |
| `depends-on` | Step dependency (runs after)             | `depends-on "build"`                     |
| `mount`      | Bind mount from host                     | `mount "." "/src"`                       |
| `mount` (ro) | Read-only bind mount                     | `mount "." "/src" readonly=true`         |
| `workdir`    | Working directory inside container       | `workdir "/src"`                         |
| `cache`      | Persistent cache volume                  | `cache "gomod" "/go/pkg/mod"`            |

Dependencies between steps are resolved via topological sort, and Cicada will catch cycles and missing references before anything runs.

## CLI Usage

```bash
# Validate without running
cicada validate pipeline.kdl

# Run a pipeline
cicada run pipeline.kdl

# Run against a remote BuildKit daemon
cicada run pipeline.kdl --addr tcp://buildkit.example.com:1234

# Dry run (generate LLB without executing)
cicada run pipeline.kdl --dry-run

# Skip cache
cicada run pipeline.kdl --no-cache
```

## Development

[mise](https://mise.jdx.dev/) is the preferred way to interact with the project. All common tasks are a `mise` invocation away:

```bash
mise build        # Build the CLI binary to ./bin/cicada
mise test         # Run tests with -race
mise vet          # Run go vet
mise lint         # Run golangci-lint
mise fmt          # Format code with gofmt
mise fmt:check    # Check formatting (CI-friendly)
mise ci           # Run the full CI suite locally (build + fmt + vet + lint + test)
```

Yes, `mise ci` runs the *actual* CI checks on your machine. Locally repeatable CI -- we meant it.

## Project Layout

```text
cmd/cicada/          CLI entry point
pkg/pipeline/      Pipeline types and validation (importable)
pkg/parser/        KDL-to-Pipeline parser (importable)
internal/builder/  BuildKit LLB generation
internal/runner/   BuildKit execution engine
examples/          Example KDL pipelines
```

Packages under `pkg/` are stable and safe for external consumers to import. Packages under `internal/` are implementation details.

## Why Go?

Cicada was originally planned in Rust, and if you go back to the first few commits, you'll see that is how it started. But the container ecosystem speaks Go. BuildKit, containerd, the OCI spec libraries, Docker itself -- they're all Go projects with Go APIs. Writing Cicada in Rust would have meant maintaining FFI bindings or shelling out to CLI wrappers, or attempting to maintain complex gRPC-based session code for core functionality, trading real engineering time for a language preference.

Go gave us native BuildKit integration (LLB construction, solve API, session management) with zero glue code. The tradeoff was worth it.

## Roadmap

See [ROADMAP.md](ROADMAP.md) for what's coming next; matrix builds, modular configs, advanced caching, and more.
