# Performance and trace interpretation

## Measurement boundaries

Dedicated picker reports use benchmark schema 2. `StartupDuration` is the
end-user startup interval: the timestamp on `session.start` through the
timestamp on the actual `fzf.start` trace record. It is not the duration of
`RunPicker`, the fzf process, or process-tree cleanup. Startup budgets compare
only this metric.

`EnrichmentDuration`, when present, is the interval from `session.start` to
the terminal `zoxide.enrichment` record. Its absence is represented by an
omitted `enrichment_duration` object (and a nil sample pointer); it must not be
decoded as zero. A present zero is a valid immediate completion. `StartupDuration`,
`EnrichmentDuration`, `LifecycleDuration`, `SourceDuration`, and
`ActionDuration` use explicit nil-versus-present sample pointers; a present
zero never falls back to the legacy `Duration` field, while nil remains
distinguishable and may use that legacy field for compatibility. The optional
`LifecycleDuration` is the separate full session interval from `session.start`
to `session.close`, and includes asynchronous source completion and joined
shutdown work. Lifecycle and enrichment statistics are reported for context
and do not fail the startup budget unless a separately named budget is added.

Candidate-only reports use `SourceDuration`. They measure candidate/source
work such as a zoxide query and remain source-cost measurements, not user
startup. The legacy `p50_us`, `p95_us`, and `p99_us` fields remain as aliases
for the report's `duration_kind` (`startup`, `source`, or `action`); new
consumers should use the named `startup_duration`, `source_duration`,
`enrichment_duration`, `lifecycle_duration`, or `action_duration` objects.
This is the performance split: local startup, asynchronous source enrichment,
user action, and full lifecycle are separate measurements.

## Trace acceptance and counters

The dedicated harness accepts one trace session only. A sample must contain
exactly one `session.start`, exactly one `fzf.start`, and one `session.close`,
with matching session IDs and non-reversing UTC timestamps. CD scenarios must
have exactly one terminal `zoxide.enrichment`; scenarios without a source may
have at most one. The initial terminal source record has generation 1, a
terminal zoxide outcome, matching attempt/start/exit/process counters, and no
live process after the harness joins. Later navigation generations are
local-only with `zoxide_outcome` `not-run` and all zoxide counters zero.

Benchmark schema 2 continues to report attempts, starts, exits, total
processes, and `zoxide_max_live`, plus preview start/live counters. Fresh and
cached launch queries each have one attempt, zero or one successful start, and
max-live one per session Builder; missing or spawn failure is attempt 1/start
0. The `cp` picker never invokes zoxide. The initial-overlap fixture contains
exactly 10,000 zoxide records, while local-first startup and source completion
are measured independently.

The public navigation benchmark ID is `navigation-local-only`. Cached/fresh
and timeout settings apply only to the eligible launch query; fresh with
timeout zero makes only that query authoritative. Direct concurrent initial
fresh calls remain permit-protected while independent sessions remain
concurrent. Preview budgets measure one steady renderer and two replacements,
including sequential/live counters. External PTY/ConPTY measurement records
cancellation-to-tree-exit. Dedicated goals cover local-only startup,
present/missing/spawn-failure/blocked/timeout launch sources, local-only
navigation, and preview dispatch; compare each with its metadata and baseline.

## Sampling and qualification

Reports use 50 samples by default. Percentiles use nearest rank
`ceil(p*n)`: with 50 samples p50 is rank 25, p95 rank 48, and p99 rank 50.
Comparisons are qualified only when host metadata and the baseline fingerprint
match and the `child-spawn`, `loopback-http`, and `warm-readdir-1000`
qualification baselines have acceptable variation. Trace records provide
observations, not a latency guarantee. Interpret startup, enrichment/source,
action, and lifecycle results separately with their metadata and baseline.
