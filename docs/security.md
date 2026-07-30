# Security and resource boundary

## Reduction and filesystem ownership

Reduction is pure reduction: no filesystem inspection or creation occurs in `Reduce`. It uses cloned AddIntent base/query bytes and lexical-only reduction. Complete absolute base ancestry and query traversal checks occur only in `CreateDirectoryTree`; partial creates are cleaned up while pre-existing parents are preserved. Error-prompt-only Add proposals retain no created tree. Handle owns ordered rollback until ownership transfers to Actor.Apply; publication keeps it, otherwise generation completion before rollback prevents a rollback race. Build-failure, caller-cancel, session-cancel, stale, supersede, and close cases are tested.

The unavoidable TOCTOU boundary is concurrent namespace replacement by another process. Trace output is a caller-authorized trace sink: the caller vouches for every ancestor and target; elevated wrappers must never accept an untrusted trace path. The internal trace ID and path fields are redacted. Final-component no-follow/type/restrictive-DACL validation is defense in depth, not anchored traversal. This uses complete absolute base ancestry before creation.

## Candidate and zoxide boundary

zoxide returns absolute filesystem paths. Arbitrary legal bytes are accepted only in absolute filesystem paths; a relative row is a malformed soft failure for the whole buffer, and a virtual token never reaches this validation. The caller cause wins over a later private timeout, leaving no partial records and a process reaped. The cached session and fresh generation policies use one cancellation-aware permit per session; a cancelled waiter returns promptly without factory or attempt, and there is no package-global mutex across independent sessions. `cp` never runs zoxide. Missing/spawn failure is attempt 1/start 0; counters assert attempts, starts, exits, processes, and max-live.

The full-record authorization is required. The virtual records are navigation only: preview and final output require authoritative filesystem targets, and a `cp` mark set with a virtual record is rejected without treating its token as a path.

## IPC, process, and preview boundaries

Loopback bearer authentication uses exact routing, one Authorization value, constant-time comparison, limit plus one decoding/draining, unconditional body closure, and no connection reuse after overlimit. Token-canary tests cover arguments, stderr, logs, traces, preview/cache/temp, and noncallback environments. Controlled callback credentials are inherited only after environment sanitization; typed action/prompt grammar limits callback interpretation.

Direct files are caller-owned. Eligible pumped closers need stable pointer identity, grant emergency cleanup permission only, and a shared pointer is registered/closed once. Value and other non-identifiable closers reject before attempt/start and are never structurally compared; ordinary completion preserves ownership. Blocking closer tests return pumps/resources to baseline, while arbitrary nonclosable methods blocked forever are outside the resource guarantee.

FreeBSD kqueue validates event count plus `EV_ERROR` and `Data errno`; observer errors never become reaping. An unsupported Unix host rejects before launch. Preview containment, deadlines, output/archive/artifact limits, atomic no-replace cache publication, and validated winners bound external tools.

Every `sessionipc.Backend` method is a cooperative backend and must promptly honor its request context. Server close marks closing, cancels base/handlers, closes its listener, performs bounded Shutdown/Close, waits for tracked handlers, and does not detach backend calls. A backend that ignores cancellation violates this precondition: Go cannot forcibly stop it without a goroutine leak. This does not alter process-global signal disposition. Normal Run has no fzf version probe.
