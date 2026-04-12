# Caching

Cicada supports remote cache backends via BuildKit's cache import/export system. This lets you share build cache across CI runs, machines, or environments -- following the same `--cache-to`/`--cache-from` conventions as `docker buildx build`.

## Cache backends

Cicada supports all BuildKit cache backends:

| Backend    | Description                              |
|------------|------------------------------------------|
| `registry` | OCI registry (Docker Hub, GHCR, ECR, etc.) |
| `gha`      | GitHub Actions cache                     |
| `local`    | Local directory                          |
| `s3`       | Amazon S3 bucket                         |
| `inline`   | Embed cache metadata in the image itself |

## Basic usage

Pass `--cache-to` and `--cache-from` to `cicada run`:

```bash
cicada run pipeline.kdl \
  --cache-to type=registry,ref=ghcr.io/myorg/cache:main \
  --cache-from type=registry,ref=ghcr.io/myorg/cache:main
```

A bare image reference (no `type=`) is shorthand for `type=registry`:

```bash
cicada run pipeline.kdl \
  --cache-to ghcr.io/myorg/cache:main \
  --cache-from ghcr.io/myorg/cache:main
```

Multiple backends can be specified by repeating the flag:

```bash
cicada run pipeline.kdl \
  --cache-from type=registry,ref=ghcr.io/myorg/cache:main \
  --cache-from type=registry,ref=ghcr.io/myorg/cache:pr-42
```

## Backend examples

### Local directory

Useful for testing or sharing cache on a single machine:

```bash
cicada run pipeline.kdl \
  --cache-to type=local,dest=/tmp/cicada-cache \
  --cache-from type=local,src=/tmp/cicada-cache
```

### OCI registry

Push and pull cache layers to any OCI-compliant registry. Use `mode=max` to cache all intermediate layers (not just the final result):

```bash
cicada run pipeline.kdl \
  --cache-to type=registry,ref=ghcr.io/myorg/cache:main,mode=max \
  --cache-from type=registry,ref=ghcr.io/myorg/cache:main
```

### GitHub Actions

The `gha` backend uses the GitHub Actions cache service. Cicada auto-detects the required environment variables when they are available, so you only need to specify the scope:

| Variable | Purpose |
|---|---|
| `ACTIONS_RUNTIME_TOKEN` | Auth token (both cache API versions) |
| `ACTIONS_RESULTS_URL` | Cache service URL (v2 API, newer runners) |
| `ACTIONS_CACHE_URL` | Cache service URL (v1 API, older runners) |

```bash
cicada run pipeline.kdl \
  --cache-to type=gha,scope=main \
  --cache-from type=gha,scope=main
```

You can override the auto-detected values with explicit attributes:

```bash
cicada run pipeline.kdl \
  --cache-to type=gha,url=https://custom.example.com,token=tok-123,scope=main \
  --cache-from type=gha,scope=main
```

### Amazon S3

```bash
cicada run pipeline.kdl \
  --cache-to type=s3,bucket=my-bucket,region=us-east-1 \
  --cache-from type=s3,bucket=my-bucket,region=us-east-1
```

## Selective cache invalidation

### Per-job (CLI)

Use `--no-cache-filter` to disable cache for specific jobs by name. Other jobs are unaffected:

```bash
cicada run pipeline.kdl \
  --no-cache-filter test \
  --cache-from type=registry,ref=ghcr.io/myorg/cache:main
```

Multiple jobs can be specified by repeating the flag:

```bash
cicada run pipeline.kdl \
  --no-cache-filter test \
  --no-cache-filter lint
```

### Per-step (KDL)

Add the `no-cache` node to a step definition to always skip cache for that step:

```kdl
pipeline "my-build" {
  step "test" {
    image "golang:1.23"
    run "go test ./..."
    no-cache
  }
}
```

### Global

Use `--no-cache` to disable cache for all steps:

```bash
cicada run pipeline.kdl --no-cache
```

## Cache statistics

Pass `--cache-stats` to print a summary of cache hits and misses after the run completes:

```bash
cicada run pipeline.kdl \
  --cache-to type=local,dest=/tmp/cicada-cache \
  --cache-from type=local,src=/tmp/cicada-cache \
  --cache-stats
```

Output:

```text
Cache summary:
  build            2/4 cached (50.0%)  174ms
  test             4/7 cached (57.1%)  138ms
  export:build     4/10 cached (40.0%)  135ms
  Overall: 10/21 cached (47.6%)
```

The statistics include all BuildKit vertices (image resolution, cache import/export operations, and actual build steps). Infrastructure vertices like `docker-image://...` and `exporting cache to client directory` are never marked as cached by BuildKit, so the reported hit rate reflects total vertex-level caching rather than just build-step caching.

## Spec format

Cache specs follow the BuildKit `type=<backend>,key=value,...` format:

```text
type=<backend>[,<key>=<value>]*
```

All attributes beyond `type` are passed directly to BuildKit. Refer to the [BuildKit cache documentation](https://github.com/moby/buildkit#cache) for the full set of supported attributes per backend.

---

# Image Publishing

Jobs can publish their final container filesystem as an OCI image using the `publish` node. See the [schema reference](schema.md#publish) for the full property list.

## Push to registry

By default, `publish` pushes to a remote registry:

```kdl
job "build" {
    image "golang:1.25"
    step "compile" { run "go build -o /app ./cmd/app" }
    publish "ghcr.io/user/app:latest"
}
```

Use `insecure=true` for HTTP registries (e.g. `localhost:5000`).

## Export to Docker

The `--with-docker-export` CLI flag pipes published images into the local Docker daemon via `docker load`, making them available for `docker run`, `docker images`, and docker-compose. Each value is a job name; `*` selects all jobs with a `publish` node.

```bash
# Export all publish jobs to local Docker daemon
cicada run pipeline.kdl --with-docker-export '*'

# Export only "build" job's publish target
cicada run pipeline.kdl --with-docker-export build

# Export multiple specific jobs
cicada run pipeline.kdl --with-docker-export build --with-docker-export deploy
```

Registry push (controlled by `push` in KDL) and Docker export (controlled by the CLI flag) can be combined -- the registry push and Docker load run concurrently:

```bash
cicada run pipeline.kdl --with-docker-export build
# if "build" has publish "ghcr.io/user/app:latest", it pushes AND loads into Docker
```

## Multi-platform images

When a matrix-expanded job publishes to the same image reference from multiple platform variants, BuildKit assembles a multi-platform manifest list automatically:

```kdl
job "build" {
    image "golang:1.25"
    platform "${matrix.platform}"
    matrix { platform "linux/amd64" "linux/arm64" }
    step "compile" { run "go build -o /app ./cmd/app" }
    publish "ghcr.io/user/app:latest"
}
```

`--with-docker-export` is not supported for multi-platform publishes -- the Docker exporter cannot produce manifest lists. Cicada will return an error before solving if a multi-platform image is matched by `--with-docker-export`.

---

# Failure Modes

By default, Cicada cancels all running jobs as soon as any job fails (`--fail-fast`, enabled by default). This is the fastest way to surface errors but means independent jobs that were still running are killed immediately.

## Continuing on failure

Pass `--fail-fast=false` to let independent jobs run to completion when a sibling fails. Only jobs that directly or transitively depend on the failed job are skipped:

```bash
cicada run pipeline.kdl --fail-fast=false
```

With `--fail-fast=false`:

- **Independent jobs** continue running and may succeed or fail on their own.
- **Dependent jobs** are still skipped -- a job whose dependency failed will not attempt to run.
- **Exports and publishes** are filtered to only include artifacts from successful jobs. Failed or skipped jobs' exports and publishes are silently dropped.
- **All errors are collected** and returned as a combined error at the end, so you see every failure in a single run rather than one at a time.

### Example

Given a pipeline with three independent jobs (`lint`, `test`, `build`), if `test` fails:

| Mode | `lint` | `test` | `build` |
|------|--------|--------|---------|
| `--fail-fast` (default) | cancelled | failed | cancelled |
| `--fail-fast=false` | completes | failed | completes |

### Dependency chains

Dependency propagation is unaffected by `--fail-fast`. A failed job always prevents its dependents from running:

```kdl
pipeline "deploy" {
    job "build" { /* ... */ }
    job "test" { depends-on "build"; /* ... */ }
    job "deploy" { depends-on "test"; /* ... */ }
    job "notify" { /* independent */ }
}
```

If `build` fails with `--fail-fast=false`, `test` and `deploy` are skipped (dependency chain), but `notify` runs to completion.

---

# Retry, Timeouts, and Shell

Cicada supports per-job retry, timeouts at both job and step level, and custom shell overrides. This section shows common usage patterns; see the [schema reference](schema.md#retry) for the full property list and inheritance rules.

## Retry

Add a `retry` node to a job to automatically re-run it on failure. Configure `attempts` (number of retries after the initial run), `delay` (wait before the first retry), and `backoff` (how delay scales across retries):

### Step-level retry and `allow-failure`

`retry` also works at the step level to re-run a single step without restarting the whole job. Add `allow-failure` alongside it so that a step exhausting its retries records a warning instead of failing the job:

```kdl
job "test" {
    image "alpine:3.19"
    step "flaky" {
        run "sh -c 'exit 1'"
        retry {
            attempts 2
            delay "1s"
        }
        allow-failure
    }
    step "must-pass" {
        run "echo success"
    }
}
```

After `flaky` exhausts all retries, its status becomes `[w]` (allowed failure) in the progress display and subsequent steps continue. The overall job passes as long as no non-`allow-failure` step fails.

```kdl
job "integration" {
    image "node:22"
    retry {
        attempts 3
        delay "5s"
        backoff "exponential"
    }
    step "test" { run "npm test" }
}
```

Backoff strategies (with a base delay of `5s`):
- `"none"` -- constant delay: 5s, 5s, 5s
- `"linear"` -- delay grows linearly: 5s, 10s, 15s
- `"exponential"` -- delay doubles each retry: 5s, 10s, 20s

## Timeouts

**Job-level timeout** caps the total wall-clock time for a job, including all retries:

```kdl
job "slow-build" {
    image "golang:1.25"
    timeout "10m"
    step "compile" { run "make build" }
}
```

**Step-level timeout** caps each `run` command within a step:

```kdl
job "test" {
    image "golang:1.25"
    step "unit" {
        timeout "5m"
        run "go test -race ./..."
    }
}
```

When both are set, the step timeout applies per-command and the job timeout applies to the overall job (including retries). Combining retry with timeout lets you bound flaky jobs:

```kdl
job "flaky" {
    image "alpine:latest"
    timeout "2m"
    retry { attempts 3; delay "5s" }
    step "run" { run "./flaky-script.sh" }
}
```

The duration format follows Go's `time.ParseDuration` syntax (e.g. `"30s"`, `"5m"`, `"1h30m"`). See the [schema reference](schema.md#timeouts) for container requirements and step-level timeout limitations.

## Shell

Override the default shell (`/bin/sh -c`) in the `defaults` block, on a `job`, or on a `step`. Step-level `shell` overrides job-level, which overrides the `defaults` block:

```kdl
defaults {
    shell "/bin/bash" "-e" "-o" "pipefail" "-c"
}

job "strict" {
    image "ubuntu:24.04"
    shell "/bin/bash" "-e" "-c"
    step "build" { run "make build" }
    step "compat" {
        shell "/bin/sh" "-c"
        run "echo 'plain sh here'"
    }
}
```

The last argument should be `-c` so the shell accepts a command string. See the [schema reference](schema.md#shell) for the full inheritance chain and argument validation rules.

---

# Secrets

Secrets let you inject sensitive values (tokens, passwords, keys) into pipeline steps without baking them into the KDL file or the container layer cache. Values are resolved on the host before the build starts and delivered to BuildKit over an encrypted session channel; they are never written to the cache.

## Declaring secrets

Secrets are declared at the pipeline level with a `source` child that specifies how the value is obtained:

```kdl
// From a host environment variable (var defaults to the secret name)
secret "API_TOKEN" {
    source "hostEnv"
}

// From a host env var with a different name
secret "REGISTRY_PASS" {
    source "hostEnv"
    var "DOCKER_PASSWORD"
}

// From a file on the host (supports ~/ expansion)
secret "SSH_KEY" {
    source "file"
    path "~/.ssh/id_rsa"
}

// From the stdout of a shell command
secret "VAULT_TOKEN" {
    source "cmd"
    cmd "vault kv get -field=value secret/ci/token"
}
```

## Injecting secrets

Reference a declared secret inside a `job` or `step` with `env` (inject as environment variable) or `mount` (inject as a file):

```kdl
secret "API_TOKEN" { source "hostEnv" }

step "deploy" {
    image "alpine:latest"
    secret "API_TOKEN" env="API_TOKEN"
    run "curl -H \"Authorization: Bearer $API_TOKEN\" https://api.example.com/deploy"
}

step "git-push" {
    image "alpine:latest"
    secret "SSH_KEY" mount="/root/.ssh/id_rsa"
    run "ssh-keyscan github.com >> /root/.ssh/known_hosts && git push"
}
```

Secret values are automatically redacted from log output. See the [schema reference](schema.md#secret-pipeline-level-declaration) for the full property list.

---

# Inter-Job Output

Jobs can pass values to downstream jobs by writing `KEY=VALUE` lines to the file at `$CICADA_OUTPUT`:

```kdl
step "check" {
    image "alpine:latest"
    run "echo should_deploy=yes >> $CICADA_OUTPUT"
}

job "deploy" {
    image "alpine:latest"
    depends-on "check"
    when "output('check', 'should_deploy') == 'yes'"
    step "push" { run "deploy.sh" }
}
```

`$CICADA_OUTPUT` is a host-managed file. Values written to it are available to dependent jobs via the `output(job, key)` CEL function in `when` conditions. Dependent jobs also source the file before their first step, making all keys available as environment variables.

---

# Partial Runs

Use `--start-at` and `--stop-after` to run a subset of jobs. Both accept either a job name or a `job:step` pair.

## Resuming after failure

Run from a specific job forward, skipping work that already succeeded:

```bash
cicada run pipeline.kdl --start-at test
```

## Running up to a checkpoint

Stop after a specific job (and its transitive dependencies):

```bash
cicada run pipeline.kdl --stop-after build
```

## Step-level granularity

Narrow the window to a specific step within a job:

```bash
# Run the "quality" job starting from the "vet" step
cicada run pipeline.kdl --start-at quality:vet

# Run the "quality" job only through the "fmt" step
cicada run pipeline.kdl --stop-after quality:fmt

# Run just the steps between "vet" and "bench" in "quality"
cicada run pipeline.kdl --start-at quality:vet --stop-after quality:bench
```

When `--start-at` targets a step, earlier steps in that job still execute (for container state) but have their exports stripped. When `--stop-after` targets a step, later steps in that job are truncated and downstream jobs are excluded.

See the [schema reference](schema.md#filtering) for full semantics.

---

# Offline Builds

Pre-warm the BuildKit image cache with `cicada pull`, then use `--offline` to prevent any network image pulls during the build:

```bash
# Pull all images referenced by the pipeline
cicada pull pipeline.kdl

# Run without network image fetches; fail hard on cache miss
cicada run pipeline.kdl --offline
```

`--offline` causes the solve to fail immediately if any required image is not present in the BuildKit cache. This is useful for reproducible builds in air-gapped environments or to detect unintended image pull regressions.

---

# Observability

Cicada emits OpenTelemetry traces for pipeline execution. Each job and step becomes a span, linked to BuildKit vertex timing.

## Sending traces to a collector

```bash
# Send to an OTLP gRPC endpoint (e.g. local Jaeger or OTEL Collector)
cicada run pipeline.kdl --trace-endpoint localhost:4317

# Or set via environment variable
OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 cicada run pipeline.kdl
```

## Writing traces to a file

```bash
cicada run pipeline.kdl --trace-file traces.json
```

The file contains OTEL span JSON, one span per line.

## Debug mode

```bash
cicada run pipeline.kdl --trace
```

Writes span JSON to stderr. Useful for quick inspection without a collector.
