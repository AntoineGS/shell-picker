# Architecture

## Session model

Each session owns one *candidate.Builder, configures it before use, and it must not be copied after first use. Startup is local-first streaming startup: the local generation is published to the input stream first, fzf begins live query filtering, and one asynchronous zoxide source resolves without blocking that publication. Only the initial CD view may admit the source records; an initial-view-only discard leaves the active/base generation unchanged. Its fresh and cached policy and timeout apply only to that source, which overlaps its single attempt with local enumeration; fresh plus timeout zero is authoritative only there. Every later navigation generation is local-only, including a return to the launch path, and reports zoxide_outcome `not-run` with zero zoxide counters. Direct concurrent initial fresh calls remain protected by the shared builder's cancellation-aware permit: a cancelled waiter returns its context cause without factory or attempt. Independent sessions may query concurrently because there is no package-global mutex. By design, CP never invokes zoxide.

The source has one terminal lifecycle record, whether it publishes a new actor generation, finds only duplicates, is discarded by navigation/input close/terminal shutdown, or fails. Picker shutdown closes the input stream, cancels the source, closes IPC, closes the actor, and joins the source before returning; this is the shutdown and join guarantee for every started source.

Candidate records carry an authoritative target independently of their wire payload: filesystem targets may preview or become final output; a Windows root virtual `..` targets Drives for navigation only. Drives has no dot record.

Insert-preserving navigation keeps Insert active, Normal navigation keeps Normal active, and successful Add navigation returns to Normal. Exact slash resolution reads only authorized immediate child records from the immutable snapshot; reduction never performs filesystem access. Normal Escape intentionally aborts the picker rather than applying the legacy clear-multi effect.

## State and Add ownership

Reduction is deliberately pure. Its exclusive branches contain either a cloned `AddIntent` or a complete `ProposedTransition`, and each reduction reads exactly one immutable snapshot. The event path calls `Reduce` exactly once, `CreateDirectoryTree` exactly once for Add, and `Actor.Apply` exactly once. No unresolved intent is passed to the actor.

`Reduce` performs no filesystem inspection or call, rollback, actor call, goroutine, or mutation. Discarding an intent creates nothing. `Handle` resolves each `AddIntent` before calling `Actor.Apply`; only the resulting `ProposedTransition` can become pending. `Handle` owns rollback before Apply, then ownership transfers to `Actor.Apply`. Ignored events are ordinary `ProposedTransition` values carrying `Effect.Ignore`. The actor retains a published tree or waits for generation completion before rollback.

A proposal carries the base generation, next state, optional build request, effect, and optional created tree. A build proposal may become the actor's one pending transition, which owns its reply channel, effect/build state, cancellation cause, and retiring/replacement state. Snapshot and resolve requests remain read-only while a build runs. Replacement ordering is cancel, wait, rollback an unowned tree, reply to the prior caller, then start the replacement. This prevents a generator from observing a removed tree; synchronous proposals publish without entering build-pending state.

```stateDiagram
[*] --> Ready
Ready --> Ready: ProposedTransition without build
Ready --> Pending: ProposedTransition
Pending --> Ready: cancel, wait, rollback, reply, replacement
Pending --> Published: build completes, effect publishes
Published --> Ready
```

Pending state owns the proposal/build context, pending reply, cancellation function, accepted generation, retiring flag, and replacement cause. Reads use the immutable published snapshot while Pending is active.

Every preview child uses `ContainmentInheritTree` under its callback tree.

Zoxide uses `ContainmentOwnTree`.

Fzf uses `ContainmentForegroundTree`.

These modes keep callback cancellation, independent query cancellation, and terminal foreground ownership distinct. Preview retains one `TreeHandle` on first child start; cancellation kills that callback tree, and close releases its handle after the callback has terminated.

## Process and terminal boundaries

There is one picker parent and one fzf child. Unix foreground TTY setup uses parent-fd `Ctty`; separate child fd 3 is supplied through `ExtraFiles`, with its lifetime and restoration handled separately. `Foreground` uses a parent fd while `Setctty` uses a child fd.

Fzf renders a two-line layout with a bounded mode-only prompt and a separate location header. Startup and resize invoke the typed `d` display callback; picker-specific `i:cd` and `i:cp` commands render info text. The display callback reads the current actor snapshot without applying a transition, changing generation, rebuilding candidates, or affecting selection. Navigation effects carry the full escaped location, and the terminal adapter emits the final width-aware `change-header` action.
Fzf owns responsive preview placement through its native 80-column alternative-layout threshold. The display callback uses exported preview geometry and subtracts preview width only for a side-by-side pane.

An invalid slash result switches to the fixed local empty source and invalid preview without shell execution. `result-final` arms `change` only after that rendering completes; the resulting one-shot restore reloads the authenticated current generation without rebuilding candidates or changing session state. While initial enrichment is active, that launch-view-only restore also cancels and discards the source, keeps the exact event-qualified load reservation, resets preview/actions, and closes the original input only after the restored bytes are applied; ordinary mode transitions suppress their same-generation restore and keep enrichment streaming.

Foreground restoration locks the OS thread, uses `unix.PthreadSigmask` to save the exact prior thread mask, blocks SIGTTOU, performs bounded `TIOCSPGRP`, restores the exact prior thread mask on every path, then unlocks. It does not change process-global signal disposition or caller notification state.

Direct files stay caller-owned. Pumped streams require identifiable closers: stable pointer identity supports shared-pointer deduplication and emergency closure; non-identifiable closers are rejected before an attempt and never structurally compared. A nonclosable blocked forever case is explicitly outside the resource guarantee. Unsupported Unix rejects before launch. FreeBSD kqueue observes but does not reap, validates event count and `EV_ERROR`/`Data`, and shutdown is ordered IPC before actor.
