# Architecture

## Session model

Each session owns one *candidate.Builder, configures it before use, and it must not be copied after first use. Cached mode owns a session cache: initial local enumeration and the single attempt overlap, then later navigation reads immutable cache data. Fresh mode uses a generation-local cache factory; every completed `cd` build overlaps local enumeration and its attempt. The shared builder has one cancellation-aware permit: a cancelled waiter returns its context cause without factory or attempt. Because independent sessions may query concurrently, there is no package-global mutex. By design, cp never queries zoxide.

Candidate records carry an authoritative target independently of their wire payload: filesystem targets may preview or become final output; a Windows root virtual `..` targets Drives for navigation only. Drives has no dot record.

## State and Add ownership

Reduction is a pure reduction by deliberate design. Exclusive `Reduction` branches consume cloned `AddIntent` data and read exactly one immutable snapshot. Reduce exactly once, CreateDirectoryTree exactly once for Add, and Apply exactly once. The invariant is no unresolved AddIntent reaching the actor.

`Reduce` performs no filesystem inspection or call, rollback, actor call, goroutine, or mutation. Discarding an intent creates nothing. Standalone `Handle` creates the tree, owns rollback before Apply, and ownership transfers to `Actor.Apply`. The actor retains a published tree or waits for generation completion before rollback; cancellation ordering is cancel, wait, then rollback/reply/replacement. The service is read-only during generation and publishes synchronous transforms.

The proposal carries base generation, next state, optional build request, effect, and optionally created tree. The actor keeps one pending transition with its reply channel, effect/build state, cancellation cause, and retiring/replacement state; snapshot and resolve requests remain read-only while a build runs. A replacement first cancels the pending build, waits for completion, rolls back an unowned tree, replies to the prior caller, then starts the replacement. This ordering prevents a generator from observing a removed tree.

Containment is explicit: picker fzf uses a foreground tree, zoxide uses its own tree, and preview children either own or inherit the callback tree as required. This separates cancellation scope from ordinary parent process lifetime.

## Process and terminal boundaries

There is one picker parent and one fzf child. Unix foreground TTY setup uses parent-fd `Ctty`; separate child fd 3 is supplied through `ExtraFiles`, with its lifetime and restoration handled separately. `Foreground` uses a parent fd while `Setctty` uses a child fd.

Foreground restoration locks the OS thread, uses `unix.PthreadSigmask` to save the exact prior thread mask, blocks SIGTTOU, performs bounded `TIOCSPGRP`, restores the exact prior thread mask on every path, then unlocks. It does not change process-global signal disposition or caller notification state.

Direct files stay caller-owned. Pumped streams require identifiable closers: stable pointer identity supports shared-pointer deduplication and emergency closure; non-identifiable closers are rejected before an attempt and never structurally compared. A nonclosable blocked forever case is explicitly outside the resource guarantee. Unsupported Unix rejects before launch. FreeBSD kqueue observes but does not reap, validates event count and `EV_ERROR`/`Data`, and shutdown is ordered IPC before actor.
