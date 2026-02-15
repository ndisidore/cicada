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

The `gha` backend uses the GitHub Actions cache service. Cicada auto-detects the `ACTIONS_CACHE_URL` and `ACTIONS_RUNTIME_TOKEN` environment variables when they are available, so you only need to specify the scope:

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

### Per-step (CLI)

Use `--no-cache-filter` to disable cache for specific steps by name. Other steps are unaffected:

```bash
cicada run pipeline.kdl \
  --no-cache-filter test \
  --cache-from type=registry,ref=ghcr.io/myorg/cache:main
```

Multiple steps can be specified by repeating the flag:

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
