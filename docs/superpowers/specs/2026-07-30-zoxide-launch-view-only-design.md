# Zoxide Launch-View-Only Candidates Design

**Date:** 2026-07-30

## Goal

Show zoxide candidates only in the initial CD picker view. After the user performs any navigation, every later candidate generation in that picker session is local-only, even if the user returns to the directory where the picker started.

## Current Behavior

The initial CD build overlaps local enumeration with zoxide and merges the results. Later behavior depends on policy:

- Cached policy reads the immutable session cache and re-merges its zoxide records on every navigation.
- Fresh policy creates and loads a new cache on every CD generation.

This keeps global zoxide destinations visible after the user has entered local filesystem navigation, contrary to the intended interaction model.

## Candidate Contract

`candidate.BuildRequest.Initial` is the authority for zoxide eligibility.

- `PickerCD` with `Initial=true` retains the current local-plus-zoxide behavior.
- `PickerCD` with `Initial=false` enumerates only the requested local location.
- `PickerCP` remains local-only for every build.
- Returning to the launch directory does not restore zoxide candidates because the request is no longer initial.

The rule is generation-based, not path-based. No launch-path field or mutable session-location state is added.

## Policy Behavior

Both public policies remain accepted to avoid breaking existing invocations:

- Cached policy creates and loads its session cache only for the initial CD build. Navigation does not read cached records.
- Fresh policy creates and loads a generation-local cache only for the initial CD build. Navigation does not invoke `NewCache`, acquire the fresh permit, or start zoxide.

The fresh permit remains in the Builder. It continues to protect direct concurrent calls that mark themselves initial, avoiding unrelated concurrency and lifecycle changes.

`--zoxide-timeout 0` remains an unlimited budget for an eligible launch query. `--zoxide-policy fresh --zoxide-timeout 0` is authoritative for that launch query only, not for later generations.

## Metrics

Every noninitial CD build reports:

- The measured local enumeration duration.
- `zoxide_outcome` as `not-run`.
- Zero attempts, starts, exits, processes, live processes, and maximum live processes.
- `ZoxideDiscarded=false` because no zoxide result was attempted or discarded.

Initial-build metrics and soft-failure behavior remain unchanged. A missing executable, spawn failure, malformed output, timeout, or cancellation on the initial query does not trigger a retry during navigation.

## Implementation Shape

Keep the policy-independent local-only branch in `candidate.Builder.Build`, before cached-navigation cache access and before fresh permit acquisition. The branch calls local enumeration once and returns its records and local-only metrics.

Do not filter zoxide records after a merged build. Filtering would preserve unnecessary process/cache work and allow hidden zoxide work to affect navigation latency and error behavior.

Do not remove the public cached/fresh flags in this change.

## Performance Contracts

Post-launch navigation has one policy-independent performance contract. Replace the obsolete `cached-navigation`, `fresh-navigation`, and `fresh-exact-parity-navigation` benchmark scenarios with `navigation-local-only`.

The replacement scenario starts from a completed initial build, performs one navigation build, and requires zero zoxide attempts or processes for that measured generation. Startup scenarios continue to cover successful, missing, failed, and timed-out launch queries.

Update the dedicated performance workflow, benchmark allowlists, baseline checks, and performance documentation to use the new scenario ID. Do not preserve misleading aliases unless a concrete external consumer is identified.

## Documentation

Update `README.md`, `docs/architecture.md`, `docs/adapters.md`, and `docs/performance.md` so they state:

- zoxide candidates appear only in the initial CD view;
- all navigation is local-only;
- cached/fresh policy selection and timeout apply only to the launch query;
- CP never invokes zoxide;
- navigation metrics report zoxide as not run.

Historical implementation plans remain unchanged.

## Testing

- Add a cached-policy regression proving a noninitial build returns local records only and does not call cache records for publication.
- Add a fresh-policy regression proving a noninitial build does not call `NewCache`, acquire query work, or record a process attempt.
- Preserve initial cached/fresh overlap, timeout, cancellation, permit, and independent-session tests.
- Update app/integration process-budget tests so a multi-generation CD session records one eligible launch attempt and zero navigation attempts under either policy.
- Update real-fzf or session integration coverage to prove zoxide rows disappear after the first navigation publication.
- Run the complete race suite and cross-platform compile gates because Builder policy and benchmark contracts are shared by Linux and Windows.
