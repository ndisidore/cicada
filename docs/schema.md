# Pipeline Schema

Cicada pipelines are written in [KDL](https://kdl.dev). A pipeline file contains a single `pipeline` node whose children define jobs, defaults, matrix dimensions, environment variables, and includes.

## Hierarchy

```text
pipeline
├── defaults          (0..1)  pipeline-wide inherited config
├── matrix            (0..1)  pipeline-level matrix expansion
├── env               (0..N)  pipeline-level environment variables
├── include           (0..N)  import jobs from other files
├── job               (0..N)  explicit multi-step job
│   └── step          (1..N)  sequential execution units
└── step              (0..N)  bare step sugar (desugars to single-step job)
```

**Jobs** are the unit of parallelism, dependency, and matrix expansion -- each job runs in its own container. **Steps** within a job are sequential RUN layers on the same container state, like consecutive `RUN` instructions in a Dockerfile.

## Quick Example

```kdl
pipeline "ci" {
    defaults {
        image "golang:1.25"
        mount "." "/src"
        workdir "/src"
    }

    job "quality" {
        step "fmt" { run "test -z $(gofmt -l .)" }
        step "vet" { run "go vet ./..." }
    }

    job "test" {
        depends-on "quality"
        step "unit" {
            cache "go-test" "/root/.cache/go-build"
            run "go test -race ./..."
        }
    }

    job "build" {
        depends-on "test"
        step "compile" { run "go build -o /out/app ./cmd/app" }
        export "/out/app" local="./bin/app"
    }
}
```

---

## Node Reference

### `pipeline`

Top-level container. Takes a name argument.

```kdl
pipeline "my-pipeline" { ... }
```

<details><summary>Children</summary>

| Child | Description |
|-------|-------------|
| `defaults` | Pipeline-wide config inherited by all jobs |
| `matrix` | Correlated matrix expansion across all jobs |
| `env` | Pipeline-level env vars (inherited by all jobs) |
| `include` | Import jobs from fragment or pipeline files |
| `job` | Multi-step job definition |
| `step` | Bare step sugar (see [Bare Step Sugar](#bare-step-sugar)) |

</details>

---

### `defaults`

Sets pipeline-wide config. Jobs inherit these values -- `image` and `workdir` are used when the job leaves them empty; `mount` is prepended; `env` is merged (job values win on conflict).

```kdl
defaults {
    image "node:22"
    workdir "/app"
    mount "." "/app"
    env "CI" "true"
}
```

<details><summary>Children</summary>

| Child | Args | Description |
|-------|------|-------------|
| `image` | `<ref>` | Default container image |
| `workdir` | `<path>` | Default working directory |
| `mount` | `<source>` `<target>` | Bind mount (supports `readonly=true`) |
| `env` | `<key>` `<value>` | Environment variable |

</details>

---

### `job`

Groups sequential steps that share a container context. Jobs are the unit of parallelism and dependency.

```kdl
job "test" {
    image "golang:1.25"
    depends-on "build"
    step "unit" { run "go test ./..." }
    step "bench" { run "go test -bench=. ./..." }
}
```

<details><summary>Children</summary>

| Child | Args / Props | Cardinality | Description |
|-------|-------------|:-----------:|-------------|
| `image` | `<ref>` | 0..1 | Container image (overrides defaults) |
| `workdir` | `<path>` | 0..1 | Working directory (overrides defaults) |
| `platform` | `<spec>` | 0..1 | OCI platform (e.g. `linux/amd64`) |
| `depends-on` | `<job-name>` | 0..N | Job dependency |
| `mount` | `<source>` `<target>` | 0..N | Bind mount (`readonly=true` supported) |
| `cache` | `<id>` `<target>` | 0..N | Persistent cache volume |
| `env` | `<key>` `<value>` | 0..N | Environment variable |
| `export` | `<container-path>` `local=<host-path>` | 0..N | Export file/dir to host |
| `artifact` | `<from-job>` `<source>` `<target>` | 0..N | Import file from dependency |
| `matrix` | (children) | 0..1 | Job-level matrix expansion |
| `no-cache` | (none) | 0..1 | Disable caching for all steps |
| `publish` | `<image-ref>` | 0..1 | Publish job filesystem as OCI image (see [publish](#publish)) |
| `step` | `<name>` | 1..N | Sequential execution unit |

</details>

---

### `step` (within a job)

A single execution unit. Steps run sequentially and share the job's container state -- each step sees filesystem changes from prior steps.

```kdl
step "install" {
    cache "node-modules" "/app/node_modules"
    run "npm ci"
}
```

<details><summary>Children</summary>

| Child | Args / Props | Cardinality | Description |
|-------|-------------|:-----------:|-------------|
| `run` | `<command>` | 1..N | Shell command (each runs in a fresh shell; a failure halts the step; filesystem state carries forward, shell state does not) |
| `env` | `<key>` `<value>` | 0..N | Step-scoped env (additive to job) |
| `workdir` | `<path>` | 0..1 | Set workdir from this step onward (like Docker `WORKDIR`) |
| `mount` | `<source>` `<target>` | 0..N | Step-specific bind mount (additive to job) |
| `cache` | `<id>` `<target>` | 0..N | Step-specific cache volume (additive to job) |
| `export` | `<container-path>` `local=<host-path>` | 0..N | Export to host (resolved from job's final state; see [Execution Model](#execution-model)) |
| `artifact` | `<from-job>` `<source>` `<target>` | 0..N | Import file from dependency |
| `no-cache` | (none) | 0..1 | Disable caching for this step |

</details>

**What stays job-only:** `image`, `platform`, `depends-on`, `publish`, and `matrix` are container-identity or DAG concerns that don't vary per step.

---

### `publish`

Publishes the job's final container filesystem as an OCI image. Takes one argument (the image reference) and optional properties.

```kdl
// Push to registry (default)
publish "ghcr.io/user/app:latest"

// Local only -- store in BuildKit, skip registry push
publish "myapp:dev" push=false
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `push` | bool | `true` | Push to remote registry |
| `insecure` | bool | `false` | Allow insecure (HTTP) registry |

When multiple matrix variants publish to the same image reference, BuildKit assembles them into a multi-platform manifest list.

To load published images into the local Docker daemon, use the `--with-docker-export` CLI flag on `cicada run` (see [usage](usage.md#export-to-docker)). Docker export is not supported for multi-platform publishes (the Docker exporter cannot produce manifest lists).

---

### `matrix`

Expands jobs into variants from the cartesian product of dimensions. Dimension values are referenced with `${matrix.<name>}`.

**Pipeline-level** matrix is _correlated_ -- dependencies between jobs are preserved per combination:

```kdl
pipeline "cross-platform" {
    matrix {
        platform "linux/amd64" "linux/arm64"
    }
    step "build" {
        image "golang:1.25"
        platform "${matrix.platform}"
        run "go build ./..."
    }
    step "test" {
        depends-on "build"
        image "golang:1.25"
        platform "${matrix.platform}"
        run "go test ./..."
    }
}
// Produces: build[platform=linux/amd64] -> test[platform=linux/amd64]
//           build[platform=linux/arm64] -> test[platform=linux/arm64]
```

**Job-level** matrix is _independent_ -- expands that job into variants, and dependents fan-in to all variants:

```kdl
job "test" {
    matrix { go-version "1.24" "1.25" }
    image "golang:${matrix.go-version}"
    depends-on "lint"
    step "test" { run "go test ./..." }
}
// Produces: lint -> test[go-version=1.24]
//           lint -> test[go-version=1.25]
```

---

### `include`

Imports jobs from another KDL file (fragment or pipeline).

```kdl
include "./fragments/lint.kdl"
include "./fragments/go-test.kdl" as="tests" {
    go-version "1.25"
    coverage-threshold "80"
}
```

| Property | Description |
|----------|-------------|
| `as` | Alias for dependency references (defaults to included file's name) |
| `on-conflict` | `"error"` (default) or `"skip"` for duplicate job names |

Child nodes are passed as parameters to fragments. `depends-on "tests"` resolves to the terminal jobs of the included fragment.

---

### `fragment`

Reusable job collection with optional parameters. Lives in its own file.

```kdl
fragment "go-quality" {
    param "go-version" default="1.25"

    job "lint" {
        image "golangci/golangci-lint:latest"
        mount "." "/src"
        workdir "/src"
        step "lint" { run "golangci-lint run ./..." }
    }

    job "test" {
        image "golang:${param.go-version}"
        depends-on "lint"
        mount "." "/src"
        workdir "/src"
        step "test" { run "go test -race ./..." }
    }
}
```

Parameters are referenced with `${param.<name>}` and substituted in all string fields. The `param` node accepts a `default=<value>` property; without it, the parameter is required.

---

## Bare Step Sugar

A `step` directly under `pipeline` is shorthand for a single-step job. This keeps simple pipelines concise and preserves backward compatibility.

```kdl
// These are equivalent:
step "build" {
    image "golang:1.25"
    mount "." "/src"
    run "go build ./..."
}

job "build" {
    image "golang:1.25"
    mount "." "/src"
    step "build" {
        run "go build ./..."
    }
}
```

A bare step accepts the union of job and step fields. `run` goes to the inner step; everything else (`image`, `depends-on`, `mount`, `cache`, `env`, `export`, `artifact`, `platform`, `matrix`, `workdir`, `no-cache`, `publish`) goes to the job.

---

## Special Variables

| Variable | Scope | Description |
|----------|-------|-------------|
| `${matrix.<dim>}` | Anywhere in a matrix-expanded job | Replaced with dimension value |
| `${param.<name>}` | Fragment jobs | Replaced with parameter value |
| `$CICADA_OUTPUT` | Runtime env | Path to output file for passing env vars to dependents |

Jobs can pass values to dependents by writing `KEY=VALUE` lines to `$CICADA_OUTPUT`. Dependent jobs automatically source these files before their first step.

---

## Inheritance & Merge Rules

| Field | Defaults -> Job | Job -> Step |
|-------|:-:|:-:|
| `image` | Fill if empty | N/A (job-only) |
| `workdir` | Fill if empty | Step overrides job |
| `mount` | Prepend | Additive |
| `cache` | N/A | Additive |
| `env` | Merge (job wins) | Additive |

---

## Execution Model

1. **Defaults** are applied to all jobs
2. **Matrix** expansion produces concrete job variants
3. **Validation** checks names, images, deps, cycles
4. **Topological sort** determines execution order
5. **Jobs** run in parallel (respecting `depends-on` and `--parallelism`)
6. **Steps** within a job run sequentially, sharing container state
7. **Exports** are solved after all jobs complete. Step-level `export` declarations are convenience syntax for organizing which paths to export; all exports resolve from the job's final container state (after all steps), not from intermediate step state

### Filtering

`--start-at` and `--stop-after` accept either a `<job>` name or a `<job>:<step>` pair to select a subgraph of jobs. Transitive dependencies are included automatically (with exports stripped to avoid side effects).

**Job-level** (e.g. `--start-at quality`): selects from that job forward (or up to that job for `--stop-after`).

**Step-level** (e.g. `--stop-after quality:fmt`): applies finer trimming within the targeted job:

| Flag | Behavior |
|------|----------|
| `--start-at job:step` | Run this job from the named step forward (earlier steps still execute for container state but have exports stripped). Downstream jobs are included. |
| `--stop-after job:step` | Run this job up to and including the named step (later steps are truncated). Downstream jobs are excluded. |

Both flags can target steps within the same job (e.g. `--start-at quality:vet --stop-after quality:bench`) to create a step window. The start step must come before or equal the stop step in declaration order.

**Colon in matrix names:** A colon inside brackets is part of the job name (e.g. `build[platform=linux/amd64:latest]`), not a job:step separator.
