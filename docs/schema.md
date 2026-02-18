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
| `env` | Pipeline-level env vars (inherited by all jobs; accessible via `env()` in `when` conditions) |
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
| `shell` | `<arg>...` | Default shell (see [Shell](#shell)) |

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
| `artifact` | `<from-job>` `source=<source>` `target=<target>` | 0..N | Import file from dependency |
| `matrix` | (children) | 0..1 | Job-level matrix expansion |
| `when` | `<CEL-expression>` | 0..1 | Conditional execution ([CEL](#when-conditions)) |
| `no-cache` | (none) | 0..1 | Disable caching for all steps |
| `publish` | `<image-ref>` | 0..1 | Publish job filesystem as OCI image (see [publish](#publish)) |
| `timeout` | `<duration>` | 0..1 | Job-level timeout (see [Timeouts](#timeouts)) |
| `retry` | (children) | 0..1 | Retry on failure (see [Retry](#retry)) |
| `shell` | `<arg>...` | 0..1 | Override default shell (see [Shell](#shell)) |
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
| `artifact` | `<from-job>` `source=<source>` `target=<target>` | 0..N | Import file from dependency |
| `when` | `<CEL-expression>` | 0..1 | Conditional execution ([CEL](#when-conditions)); `output()` not allowed at step level |
| `timeout` | `<duration>` | 0..1 | Step-level timeout (see [Timeouts](#timeouts)) |
| `shell` | `<arg>...` | 0..1 | Override shell for this step (see [Shell](#shell)) |
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

### Timeouts

Timeouts can be set at the job level, the step level, or both.

**Job-level timeout** cancels the entire job (including retries) after the specified duration:

```kdl
job "slow-build" {
    image "golang:1.25"
    timeout "10m"
    step "compile" { run "make build" }
}
```

**Step-level timeout** wraps individual `run` commands so they are killed if they exceed the duration:

```kdl
job "test" {
    image "golang:1.25"
    step "unit" {
        timeout "5m"
        run "go test -race ./..."
    }
}
```

When both are set, the step timeout applies per-command and the job timeout applies to the overall job. The duration format follows Go's `time.ParseDuration` syntax (e.g. `"30s"`, `"5m"`, `"1h30m"`).

**Limitation:** Step-level timeouts use the `timeout` command (from GNU coreutils or BusyBox) inside the container. The container image must provide `timeout` in its `$PATH`. Most common base images (Alpine, Debian, Ubuntu, etc.) include it, but minimal images like `scratch` or `distroless` do not. If your image lacks `timeout`, use a job-level timeout instead, or install coreutils in an earlier step.

---

### Retry

Configures automatic retry on job failure. The `retry` node accepts child nodes for `attempts`, `delay`, and `backoff`.

```kdl
job "flaky-integration" {
    image "node:22"
    retry {
        attempts 3
        delay "5s"
        backoff "exponential"
    }
    step "test" { run "npm test" }
}
```

| Child | Type | Default | Description |
|-------|------|---------|-------------|
| `attempts` | int | `1` | Number of retry attempts (in addition to the initial run) |
| `delay` | duration | `0` | Wait time before the first retry |
| `backoff` | string | `"none"` | Delay scaling: `"none"`, `"linear"`, or `"exponential"` |

Backoff strategies scale the delay for subsequent retries (n is 0-indexed: first retry n=0, second retry n=1, etc.):
- **none**: constant delay on every retry
- **linear**: delay × (n+1), e.g. 5s, 10s, 15s for n=0,1,2
- **exponential**: delay × 2^n, e.g. 5s, 10s, 20s for n=0,1,2

Retry interacts with job-level timeout: the timeout covers all attempts combined, so a job with `timeout "1m"` and `retry { attempts 3 }` gets at most 1 minute total across all attempts.

---

### Shell

Overrides the default shell (`/bin/sh -c`) used to execute `run` commands. It can be set in `defaults` (pipeline-wide), on a `job`, or on a `step`. Step-level overrides job-level, which overrides defaults.

```kdl
defaults {
    shell "/bin/bash" "-e" "-o" "pipefail" "-c"
}

job "strict" {
    image "ubuntu:24.04"
    shell "/bin/bash" "-e" "-c"
    step "build" { run "make build" }
    step "test" {
        shell "/bin/sh" "-c"
        run "echo 'using plain sh for this step'"
    }
}
```

The last argument should typically be `-c` so the shell accepts a command string. All arguments are passed as the exec entry point; the `run` command string is appended as the final argument.

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

A bare step accepts the union of job and step fields. `run` goes to the inner step; everything else (`image`, `depends-on`, `mount`, `cache`, `env`, `export`, `artifact`, `platform`, `matrix`, `workdir`, `no-cache`, `publish`, `when`, `timeout`, `retry`, `shell`) goes to the job.

---

## When (Conditions)

The `when` node enables conditional execution using [Google's Common Expression Language (CEL)](https://github.com/google/cel-go). CEL is a non-Turing-complete, sandboxed expression language familiar to Kubernetes/platform engineers.

```kdl
// Skip job unless on main or release branch
job "deploy" {
    when "branch.matches('^(main|release/.*)$')"
    image "alpine:latest"
    step "push" {
        run "deploy.sh"
    }
}

// Skip step unless CI environment
step "slow-tests" {
    image "golang:1.25"
    when "hostEnv('CI') == 'true'"
    run "go test -tags=slow ./..."
}
```

### CEL Environment

| Binding | Type | Description |
|---------|------|-------------|
| `branch` | `string` | Current git branch (empty if detached/unknown) |
| `tag` | `string` | Git tag pointing at HEAD (empty if none) |
| `env(key)` | `function(string) -> string` | Pipeline-declared env var for the current scope (pipeline + defaults + job + step merge hierarchy; empty if unset) |
| `hostEnv(key)` | `function(string) -> string` | Host OS env var value (empty if unset); reads from the host process environment |
| `output(job, key)` | `function(string, string) -> string` | Dependency job's `$CICADA_OUTPUT` value (empty if unset/job skipped) |
| `matrix(key)` | `function(string) -> string` | Matrix dimension value for current variant (empty if not a matrix job or key unset) |

Available CEL operators and functions include: `string.matches(regex)` (RE2), `string.startsWith()`, `string.endsWith()`, `string.contains()`, `size()`, `==`, `!=`, `in`, `? :`, `&&`, `||`, `!`.

### Two-Phase Evaluation

Conditions are split into **static** and **dynamic**:

| Phase | Timing | Available bindings | Applies to |
|-------|--------|-------------------|------------|
| **Static** | Pre-build | `branch`, `tag`, `env()`, `hostEnv()`, `matrix()` | Jobs + steps |
| **Dynamic** | Runtime (after deps complete) | All static + `output()` | Jobs only |

The parser detects whether `when` references `output()` and marks it as deferred. Static conditions are evaluated before the build phase; deferred conditions are evaluated at runtime after dependencies complete. In both phases, `matrix()` reflects the current variant's dimension values.

### Semantics

- **Skipped job (static)**: when a job is skipped by a static `when` condition (evaluated pre-build by `EvaluateConditions`), it is treated as succeeded. Dependents still run; dependency edges and artifact imports from the skipped job are pruned with a warning. Skipped jobs produce empty outputs.
- **Skipped job (deferred)**: when a job is skipped at runtime by a deferred `when` condition (evaluated by the runner after dependencies complete), the skip cascades -- all downstream dependents are also skipped.
- **Step-level skip**: subsequent steps in the same job continue.
- If all steps in a job are conditionally skipped, the job itself is skipped.
- One `when` per job/step. Use `&&`/`||` within the expression for compound logic.
- `output()` is only allowed in **job-level** `when` (not step-level).
- `env()` reads **pipeline-declared** env vars scoped to the evaluation context (pipeline + defaults + job + step, following the merge hierarchy). Use `hostEnv()` to read host OS environment variables.

### Dynamic Conditions with `output()`

```kdl
step "check" {
    image "alpine:latest"
    run "echo should_deploy=yes >> $CICADA_OUTPUT"
}

job "deploy" {
    image "alpine:latest"
    depends-on "check"
    when "output('check', 'should_deploy') == 'yes'"
    step "push" {
        run "deploy.sh"
    }
}
```

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
