# TUI Progress Display

Cicada renders a live progress display while running pipelines. The display uses
two icon sets depending on terminal capability: emoji icons for interactive TTYs
and compact ASCII icons for non-TTY output (CI logs, piped output, etc.).

## Status Icons

| Status          | Emoji | ASCII | Meaning                                          |
|-----------------|-------|-------|--------------------------------------------------|
| Done            | ✅    | `[+]` | Step completed successfully                      |
| Running         | 🔨    | `[~]` | Step is currently executing                      |
| Cached          | ⚡     | `[=]` | Step result was served from cache                |
| Pending         | ⏳    | `[ ]` | Step is waiting to start                         |
| Error           | 🚨    | `[!]` | Step failed                                      |
| Timeout         | ⏰    | `[t]` | Step exceeded its configured timeout             |
| Retry           | 🔄    | `[r]` | Step or job is being retried                     |
| Allowed Failure | ⚠️    | `[w]` | Step failed but `allow-failure` prevented job failure |

## Job Header

Each job shows a header line with progress and optional annotations:

```text
Job: build (3/5)  1.2s
```

- `(3/5)` -- 3 of 5 steps resolved (done, cached, error, timeout, or allowed failure)
- `1.2s` -- wall-clock duration (shown when the job completes)
- `[r] attempt 2/3` -- retry annotation (shown during job-level retries)
- `[t] timed out (30s)` -- timeout annotation (shown when the job times out)

## Boring Mode

Pass `--boring` to force ASCII icons and a simple pipe spinner (`|`, `/`, `-`,
`\`) instead of the default braille dot spinner. This is auto-selected when
output is not a TTY.
