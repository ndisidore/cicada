# CLI Reference

Complete reference for every `cicada` command and flag.

---

## Global flags

These flags apply to all commands.

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `--runtime` | `CICADA_RUNTIME` | `auto` | Container runtime: `auto`, `docker`, `podman`. `auto` tries Docker then Podman. |
| `--format` | `CICADA_FORMAT` | `auto` | Output format: `auto`, `pretty`, `json`, `text`. `auto` selects `pretty` on a TTY, `text` otherwise. |
| `--log-level` | `CICADA_LOG_LEVEL` | `info` | Log verbosity: `debug`, `info`, `warn`, `error`. |

---

## `cicada validate`

Parse and validate a KDL pipeline file without executing it.

```bash
cicada validate <file | ->
```

Reads from stdin when the argument is `-`. Exits non-zero on any parse or validation error.

---

## `cicada visualize`

Render a pipeline as a dependency graph.

```bash
cicada visualize <file | ->
```

Without `--output`, renders Unicode box-drawing to the terminal.

| Flag | Description |
|------|-------------|
| `--output`, `-o` | Write to a file. Supported extensions: `.d2` (D2 diagram language), `.dot` (Graphviz DOT). |

---

## `cicada run`

Build and execute a pipeline against BuildKit.

```bash
cicada run <file | -> [flags]
```

### BuildKit connection

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | platform-specific socket | BuildKit daemon address: Unix socket path or `tcp://host:port`. |
| `--no-daemon` | `false` | Disable automatic buildkitd management; connect to `--addr` directly. |
| `--progress` | `auto` | Progress output mode: `auto`, `tui`, `plain`, `quiet`. `auto` selects `tui` on an interactive TTY, `plain` otherwise. |

### Run behavior

| Flag | Default | Description |
|------|---------|-------------|
| `--dry-run` | `false` | Build the LLB graph without executing. |
| `--boring` | `false` | Use ASCII icons instead of emoji in TUI output. |
| `--parallelism`, `-j` | `0` (unlimited) | Maximum number of concurrently running jobs. |
| `--fail-fast` | `true` | Cancel all running jobs on first failure. Pass `--fail-fast=false` to let independent jobs finish. |
| `--expose-deps` | `false` | Mount full dependency root filesystems at `/deps/{name}` in each job. |
| `--start-at` | | Run from this job (or `job:step`) forward. |
| `--stop-after` | | Run up to and including this job (or `job:step`). |

### Cache

| Flag | Default | Description |
|------|---------|-------------|
| `--no-cache` | `false` | Disable BuildKit cache for all jobs. |
| `--no-cache-filter` | | Disable cache for specific jobs by name (repeatable). |
| `--cache-to` | | Cache export destination, e.g. `type=registry,ref=ghcr.io/user/cache`. Repeatable. Bare image refs default to `type=registry`. |
| `--cache-from` | | Cache import source. Same format as `--cache-to`. Repeatable. |
| `--cache-stats` | `false` | Print a cache hit/miss summary after the run. |
| `--no-sync-cache` | `false` | Disable content-hash file sync cache. |
| `--offline` | `false` | Fail if any required image is not cached (use `cicada pull` first). |

### Publishing

| Flag | Description |
|------|-------------|
| `--with-docker-export` | Load published images into the local Docker daemon. Accepts job names or `*` for all. Repeatable. |

### Tracing

| Flag | Env var | Description |
|------|---------|-------------|
| `--trace-endpoint` | `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP gRPC endpoint for sending traces (e.g. `localhost:4317`). |
| `--trace-file` | | Write OTEL span JSON to a file. |
| `--trace` | | Write OTEL span JSON to stderr (debug mode). |

---

## `cicada pull`

Pre-pull all images referenced by a pipeline into the BuildKit cache. Use before `cicada run --offline`.

```bash
cicada pull <file | -> [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` | platform-specific socket | BuildKit daemon address. |
| `--no-daemon` | `false` | Disable automatic buildkitd management. |
| `--progress` | `auto` | Progress output mode. |
| `--boring` | `false` | Use ASCII icons instead of emoji in TUI output. |

---

## `cicada engine`

Manage the local BuildKit engine container.

### `cicada engine start`

Start the BuildKit engine. If the engine container already exists and is stopped, it is restarted.

```bash
cicada engine start
```

### `cicada engine stop`

Stop the BuildKit engine container without removing it.

```bash
cicada engine stop
```

### `cicada engine status`

Print the current engine state (`running`, `stopped`, or not present).

```bash
cicada engine status
```
