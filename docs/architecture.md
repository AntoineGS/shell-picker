# Architecture

## Session model

Each session owns one *candidate.Builder, configures it before use, and it must not be copied after first use. Cached mode owns a session cache: initial local enumeration and the single attempt overlap, then later navigation reads immutable cache data. Fresh mode uses a generation-local cache factory; every completed `cd` build overlaps local enumeration and its attempt. The shared builder has one cancellation-aware permit: a cancelled waiter returns its context cause without factory or attempt. Because independent sessions may query concurrently, there is no package-global mutex. By design, cp never queries zoxide.

Candidate records carry an authoritative target independently of their wire payload: filesystem targets may preview or become final output; a Windows root virtual `..` targets Drives for navigation only. Drives has no dot record.

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

Foreground restoration locks the OS thread, uses `unix.PthreadSigmask` to save the exact prior thread mask, blocks SIGTTOU, performs bounded `TIOCSPGRP`, restores the exact prior thread mask on every path, then unlocks. It does not change process-global signal disposition or caller notification state.

Direct files stay caller-owned. Pumped streams require identifiable closers: stable pointer identity supports shared-pointer deduplication and emergency closure; non-identifiable closers are rejected before an attempt and never structurally compared. A nonclosable blocked forever case is explicitly outside the resource guarantee. Unsupported Unix rejects before launch. FreeBSD kqueue observes but does not reap, validates event count and `EV_ERROR`/`Data`, and shutdown is ordered IPC before actor.
