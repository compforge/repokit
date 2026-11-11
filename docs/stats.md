# Local statistics

`repocli stats` summarizes the records currently retained in `~/.repokit` across
repositories. It works outside a Git checkout and does not require historical
checkouts to still exist.

```sh
repocli stats
repocli stats --json
repocli stats --top 10
repocli stats --home /path/to/repocli-state --json
```

`--top` selects 1–100 slow command runs, default 5. `--timeout` bounds collection.
For one repository, use `repocli validate --history --repo /path/to/repo`.
`stats` rejects `--repo` rather than silently narrowing only part of the report.

## Report and counting rules

- **Storage:** regular-file count and logical bytes by top-level directory,
  skipped symlink count, oldest/newest file modification time. This is not
  allocated disk space (`du`); timestamps do not establish cache validity or
  authorize deletion. PR/HTTP caches and other retained directories contribute to
  storage only. They are not command or validation events.
- **Validation:** per-repository, per-Component/check statistics plus global
  totals. The Go toolkit's `ReadValidationHistories` reuses the same locked WAL
  replay as `validate --history`; it discovers existing repository histories
  without opening Git checkouts. Pass rate is `passed / (passed + failed)`;
  reuse rate is `reuses / (executions + reuses)`. Empty denominators report zero.
  Unfinished and other outcomes remain visible. Reuse does not add the original
  execution duration again. Execution and lookup seconds are summed check time,
  not elapsed user waiting time.
- **Commands:** terminal command outcomes grouped by resolved command path,
  total/p50/p95/max elapsed time, and slow runs with run ID, timestamp, exit code
  and (in JSON) recorded working directory. Arguments and output are not emitted.
  Starts without retained completions are counted separately; they may still be
  running. Terminal records without a retained start still count. Other slog
  messages are ignored. Malformed or duplicate command records are diagnosed.
- **Diff/impact:** schema-5 analysis statuses, operation durations and individual
  stage durations decoded through the native timeline model. Only finished,
  nonnegative intervals contribute timing samples; `untimed` counts analyses
  without a usable finished operation interval. Stage sample counts exclude
  unfinished intervals. Nested/overlapping stage totals must not be added to
  infer wall time. Analysis completion is separate from command/output success.

Percentiles use nearest rank over the retained timing samples, including failed
commands and analyses. Each section exposes its own observed time bounds; there
is no promise that all sections cover the same period. Statistics are not lifetime
totals or proof that a project is healthy.

## Observation and gaps

The command does not execute checks, contact a Forge, refresh caches, prune
history or write its own execution log. Existing validation stores use their
normal local lock/WAL access, which requires write permission for the lock.
The report is a sequential observation of live files, not an atomic snapshot
across directories. Missing state directories produce an empty report without
creating them. Collection does not enforce a new 30-day filter: old retained
records remain observable until the owning writer performs retention cleanup.

Unreadable or corrupt validation stores are excluded as a whole with diagnostics;
valid stores remain available. Malformed log records and unknown diff schemas
are excluded with per-file diagnostics; other valid records remain available.
Lines are limited to 16 MiB; a larger line diagnoses and stops that file's scan.
Child symlinks are not traversed for storage, log discovery or repository discovery.

JSON uses the [CLI result envelope](cli-output.md); the report in `data` always includes
`complete` and `diagnostics`. Partial reports are printed
and exit with status 1. Argument errors exit 2; complete reports exit 0. Fatal
collection errors such as cancellation exit 1 without claiming complete results.
