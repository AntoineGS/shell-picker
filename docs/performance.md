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

Task6 first-frame reports use an explicit `unit` on every metric object.
Duration metrics use `unit: "us"` with generic `p50`, `p95`, and confidence
interval values; callback, renderer, and sidecar counts use `unit: "count"`
with the same generic value names. Count metrics never serialize duration-
suffixed fields.

The checked-in first-frame harness is baseline-gated by default. A diagnostic
run requires the harness-only `SHELL_PICKER_FIRST_FRAME_DIAGNOSTIC=1` flag,
emits `diagnostic-unqualified`, never runs acceptance checks, and removes the
flag before launching picker or fzf children. Diagnostic reports use relative
raw-artifact names, hash host identity, and contain no user paths, credentials,
queries, picker state, or API keys.
Build qualification also records `source_head` and a deterministic
`source_fingerprint` over the checked-in and non-ignored build inputs. The
reproducible build script captures that identity before and after both builds,
rejects source drift, and removes selected outputs on a mismatch; the
checked-in harness rejects stale source metadata before measuring.

First-frame callback counts are derived only after the session-close trace
barrier has completed. The harness stores terminal timestamps with their
monotonic readings for elapsed durations, keeps a separate UTC wall-clock cutoff
for trace ordering, and counts only callback `*.start` records at or before the
meaningful frame. Preview-boundary metrics use
`through_preview_complete` and the causal `preview.finished` renderer event,
not an immediate renderer-start snapshot.

## Provisional Task6 qualification record

The experimental runtime remains opt-in and default-off. The formal status remains
`baseline-required`/`diagnostic-unqualified`; qualified rerun pending.
official mode continues to block when the host baseline is not qualified, while
diagnostic mode cannot emit a passing status.

Two 30-pair diagnostic runs varied. Enabled meaningful-frame p95 was 145 ms in
the first run and 220 ms in the second. Callback median reduced from 3 to 0 in
the first run and from 3 to 1 in the second; preview-complete median improved
by roughly 90-100 ms. First-byte p95 delta was +7 ms in the first run and
+26.8 ms in the second. The user
accepted a provisional merge despite the second run missing both the <170 ms
meaningful-frame p95 and <=+10 ms first-byte criteria. No default rollout claim
is made.

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

## Experimental fzf listen sidecar

The authenticated fzf listen sidecar is an experimental prototype and is
disabled by default. It is enabled only with the exact opt-in
`SHELL_PICKER_EXPERIMENTAL_FZF_SIDECAR=1`. Without that variable, picker
startup keeps the existing callback-based `i:cd`/`i:cp` info command and does
not create a sidecar key or add `--listen`; an inherited `FZF_API_KEY` remains
subject to the existing sanitized inherited-environment contract.
Disabled mode retains both startup and resize display callbacks.

With the opt-in, shell-picker starts fzf 0.74.1 with a numeric IPv4 loopback
`--listen` address and a per-session authenticated API key. A bounded polling
sidecar reads fzf state and posts only a validated `change-list-label` action.
The provisional list-label placement is the list border label: CD renders
`matched/total`, while CP appends ` (selected)` only when a selection exists.
The enabled path uses a static initial `--header` and omits both startup and resize `transform(d)` display callbacks. fzf's native header truncation keeps
the location usable after terminal resize; the sidecar only owns the list
label and never refreshes the picker header.

Polling uses the configured readiness interval/deadline for connection-refused
startup retries and request timeout/deadline failures retry while the parent/session context remains live; parent cancellation/deadline stops immediately. It then uses a 25ms transient retry cadence and a two-second
consecutive live window. GET/POST diagnostics are available through the closed, secret-free
observer vocabulary and schema-valid `sidecar.*` trace records: only category,
attempt count, duration, and stop reason are recorded. Typed bounded transport
resets, EOFs, request timeouts, temporary network errors, connection
refusal/abort, broken pipes, and POST 503 responses are transient; arbitrary
wrapped URL errors, 401/403, malformed/schema/oversize, and action-validation
failures remain terminal-soft. If the parent/session context cancels an
in-flight request, no GET/POST diagnostic is recorded for that operation; the
session emits the context-canceled stop diagnostic.
Successful POSTs alone complete a pending label update, so a failed POST does
not advance the last-posted label.

The prototype retains a residual port time-of-check/time-of-use race: the
sidecar reserves an ephemeral port, releases the reservation, and fzf later
binds the numeric loopback address. The API key and bounded authenticated
HTTP exchange reduce exposure but do not remove that operating-system-level
race.

The prototype criteria before broader rollout are: meaningful p95 <170ms, a
first-byte regression <=10ms, a materially reduced callback count, and >=30 samples; real fzf 0.74.1 PTY/ConPTY
coverage for CD and CP labels, query filtering, navigation, preview, accept,
and abort; authenticated fake-server coverage for malformed, unauthorized,
forbidden, oversized, and inconsistent state; deterministic generated-key
coverage in the concrete app/fake server across captured sinks; unknown-state
field coverage; and real command-line absence checks for injected canaries.
The real process tests do not claim to capture the runtime-generated key.
The rollback is to unset
`SHELL_PICKER_EXPERIMENTAL_FZF_SIDECAR` and return to the callback-only path.

## Sampling and qualification

Reports use 50 samples by default. Percentiles use nearest rank
`ceil(p*n)`: with 50 samples p50 is rank 25, p95 rank 48, and p99 rank 50.
Comparisons are qualified only when host metadata and the baseline fingerprint
match and the `child-spawn`, `loopback-http`, and `warm-readdir-1000`
qualification baselines have acceptable variation. Trace records provide
observations, not a latency guarantee. Interpret startup, enrichment/source,
action, and lifecycle results separately with their metadata and baseline.
