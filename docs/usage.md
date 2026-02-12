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
