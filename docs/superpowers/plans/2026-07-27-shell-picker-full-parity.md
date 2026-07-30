# Shell Picker Full Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a cross-platform Go shell picker that preserves the authoritative Linux `cd` and `cp` picker behavior on Linux and Windows while replacing shell-script hot paths with one parent process, authenticated callback IPC, and one fzf process.

**Architecture:** A single parent process owns a session actor containing immutable generation snapshots and executes navigation as pending proposed-state transactions that publish mode, location, prompt, generation, records, and reply atomically. Event reduction is strictly pure: ordinary events reduce to complete proposals, while valid Add reduces to a cloned filesystem-free intent that standalone `Handle` resolves exactly once before transferring one complete proposal to the actor. fzf invokes the same executable through a conservative basename resolved from a prepended `PATH`; callback subprocesses use one bearer-authenticated loopback endpoint, while preview tools run locally inside the callback's cancellation tree, so there is no shell interpreter, daemon, or fzf listener. The default zoxide policy owns one immutable session cache: the initial `cd` generation overlaps its only `zoxide query --list` invocation attempt with one batched local scan, and later navigation merges that cache with fresh local scans. An explicit fresh policy makes one invocation attempt on every `cd` generation for authoritative Linux freshness, with one cancellation-aware permit owned by the session's single `*candidate.Builder` limiting that session to one live query while independent sessions remain concurrent.

**Tech Stack:** Go 1.26.0 with toolchain 1.26.5, Go standard library, the sole module `golang.org/x/sys v0.47.0`, fzf 0.74.1 or newer, Zsh, Nushell, GitHub Actions.

## Global Constraints

- The Git module is exactly `github.com/AntoineGS/shell-picker`; `go.mod` declares `go 1.26.0` and `toolchain go1.26.5`.
- The only non-standard-library Go module is `golang.org/x/sys v0.47.0`: production Windows Job code and Windows Job/ConPTY tests import `golang.org/x/sys/windows`, Unix production process code and Linux integration tests may import `golang.org/x/sys/unix`, and no other package or module is added.
- The target remote remains `git@github.com:AntoineGS/shell-picker.git`; implementation begins on the empty unborn `main` branch.
- Linux behavior is authoritative from `/home/antoinegs/gits/configurations/Linux/fzf/fzf-picker-candidates.zsh` at SHA-256 `5300b66b7815e8b1c2f75f230033a069a1c305600faea164c9214cd52e07cb97`.
- Preview behavior is authoritative from `fzf-preview.sh` at SHA-256 `232eb46eef32bff642985e42edbf0cd3a49098e7485eb6f5b0db0bdf48024159` and `fzf-batch-encode.pl` at SHA-256 `055d9a74cce513bbf02475fae97154b159de98d51cc565cf4859ced0226878fd`.
- The 371 generated assertions in `fzf-picker.test.zsh` at SHA-256 `f920b8f6194c76d5f8a1737c6e4860ab04f641da291dc3e984cbb63443552776` are ported into checked-in parity matrix rows and differential/category fixtures.
- Zsh trigger and insertion behavior is authoritative from `Linux/zsh/.zshrc` at SHA-256 `3bc868023693945a97b2e23f8f806ae5bdaa228a9898fb86cd8ad075e559ab18`.
- Picker semantics and preview feature categories/tool precedence are parity targets: Insert, Normal, Add, safe Add-directory creation, deterministic candidate order, zoxide merging, navigation, accepted-record validation, selection order/multiplicity, abort behavior, and every preview category are covered on Linux and Windows.
- Exact renderer bytes, legacy cache-key collisions, blank output when tools are missing, shell-script defects, and locale-sensitive ordering are not parity targets; fixtures document deterministic correctness and safety fixes.
- Platform-specific differences are limited to roots and drives, native path APIs, cursor/output integration, and shell editor integration.
- Native enumeration intentionally shows ignored immediate entries in both pickers; this adopts the more useful `cd --no-ignore` behavior and removes the legacy `cp`/`cd` ignore-filter asymmetry as a documented compatibility correction.
- Each picker has one parent Go process and exactly one fzf process; normal `Run` starts no fzf version probe, and callback and preview subprocesses are short-lived invocations of the same executable.
- fzf 0.74.1 or newer is an external installation/runtime precondition: `Run` does not verify the version, behavior with an older fzf is unsupported, and `probe`, CI, and real-fzf gates verify the precondition through `CheckVersion`.
- The parent accepts a callback basename only when it matches `[A-Za-z0-9._-]+`, rejects whitespace/metacharacters and an executable directory containing `os.PathListSeparator` before fzf starts, launches the configured fzf with that basename plus `--fzf-shell`, and prepends its possibly spaced directory to callback `PATH`.
- Callback commands never invoke `sh`, `zsh`, `cmd.exe`, PowerShell, or another command interpreter; production and tests never use fzf `--listen`.
- The callback command grammar accepts only `e:OPCODE`, `l:GENERATION`, and `p`, where `GENERATION` is canonical unsigned decimal and `OPCODE` is one of `mi`, `ma`, `es`, `fw`, `up`, `sl`, `hm`, and `en`.
- Callback input comes only from `FZF_KEY`, `FZF_QUERY`, and `FZF_CURRENT_ITEM`; action strings are produced only by typed action constructors.
- Candidate wire records use one grammar: `KIND`, one tab byte, `ESCAPED_DISPLAY`, one tab byte, and `PADDED_BASE64_PAYLOAD`; records are NUL-framed to and from fzf. Filesystem kinds encode their unchanged path bytes, while `virtual` encodes only the fixed protocol target token whose exact raw bytes are `[]byte("drives")`.
- Go strings and byte slices preserve arbitrary legal POSIX pathname bytes excluding NUL, with slash reserved as the separator; no JSON field carries a raw path, query, or candidate record.
- Base64 is transport encoding, not authorization; event, preview, and final selection paths resolve by exact full-record identity against the active immutable snapshot. Payloads remain nonempty and canonical for every kind; adding the virtual token does not weaken `DecodePath` or arbitrary-path validation for filesystem kinds.
- Every authoritative `candidate.Record` and immutable snapshot preserves `Target pathutil.Location`: ordinary local, drive, and zoxide records use `pathutil.Filesystem(Path)`, while the Windows root virtual parent has no filesystem `Path` and uses `pathutil.Drives()`. The token is wire identity data, never a substitute filesystem path.
- The session actor is the only owner of mode, current location, prompt, navigation, generation counters, pending transitions, and published snapshots.
- `Reduce` is strictly pure and returns a `session.Reduction` containing either one complete ordinary `ProposedTransition` or one cloned `AddIntent`, never both unresolved. Standalone `Handle` reads exactly one immutable snapshot, calls `Reduce` exactly once, resolves an Add intent with exactly one `pathutil.CreateDirectoryTree` call, and calls `Actor.Apply` exactly once with a complete proposal; no unresolved Add intent reaches the actor. The actor keeps serving read-only snapshot/preview membership requests while one build is pending, then atomically publishes the complete proposal and replies only if that build succeeds and remains current. Supersede/close/cancel first cancels and waits for generation completion, then rolls back/replies/starts replacement, so rollback never races generator reads.
- fzf transform navigation is intentionally synchronous, so ordinary later keys cannot supersede it; cancellation exists for session abort/shutdown and malicious concurrent requests, and no actor method calls another public actor method.
- The default cached zoxide policy overlaps initial local `ReadDir` with one session cache invocation attempt and merges immutable cached paths on later navigation. `--zoxide-policy fresh` instead overlaps one new zoxide invocation attempt with every `cd` generation. Each picker session owns exactly one `*candidate.Builder`: an unconfigured dependency value may be copied once before use, then explicit cached/fresh configuration clears stale cache/factory state and, for fresh policy, eagerly allocates that session's cancellation-aware permit before the first `Build`. `Builder` contains no embedded `sync.Once`/lock that makes the sanctioned pre-use copy fail `go vet`; `Build` does not lazily create permit state. After explicit configuration the Builder is passed only by pointer and never copied. Fresh-policy calls share that one permit, limiting the session to one live zoxide query. A caller cancelled while waiting returns its context cause promptly without calling `NewCache` or recording an attempt. Independent picker sessions and Builder instances may query concurrently; no package- or process-global mutex is permitted. Local enumeration uses batches of 128 entries and symlink `Stat` workers clamped to `2..8`.
- A cancelled, failed, or maliciously superseded generation never publishes; local enumeration errors are hard, zoxide missing/process/malformed/timeout outcomes are soft, and every partial zoxide buffer is discarded before the immutable cache is published.
- The `zoxide query --list` contract returns absolute filesystem paths. Accepted rows preserve arbitrary legal bytes within a valid platform-absolute path; a relative row is malformed soft failure and discards the complete zoxide buffer. `KindVirtual` and its fixed `drives` token never reach zoxide row absolute-path validation.
- The zoxide query budget defaults to `75ms` on Linux and `150ms` on Windows. Public `--zoxide-timeout DURATION` accepts any nonnegative Go duration, with `0` selecting an unlimited query; timeout kills the owned zoxide tree, records outcome `timeout`, and contributes no partial paths. Public `--zoxide-policy cached|fresh` defaults to `cached`; `fresh` plus timeout `0` is the exact authoritative requery behavior.
- Hot candidate generation invokes no `fd`, `base64`, Perl, eza, or shell wrapper and starts no goroutine per candidate.
- IPC uses `net/http` on `tcp4` `127.0.0.1:0`, one 256-bit `crypto/rand` bearer token, 64 KiB request-body limits, fixed timeouts, and at most 16 concurrent handlers; a separate internal 128-bit session ID exists only for redacted tracing and is never exposed. Client response constants are exact: JSON event/preview/error responses are at most 64 KiB, load `application/octet-stream` responses are at most 64 MiB, and telemetry's empty response is at most 1 KiB. Every response path reads, decodes, or drains through its limit plus one, rejects overlimit before returning data, always closes the body, and prevents reuse of an overlimit connection as appropriate.
- The callback address parser accepts exactly `http://127.0.0.1:PORT` with decimal nonzero `PORT` and rejects userinfo, nonempty path, query, fragment, alternate hosts, and alternate schemes.
- Server routing accepts only an exact `RequestURI` equal to `/v1/event`, `/v1/load`, or `/v1/preview`, with both `RawQuery` and `RawPath` empty. It rejects a query, encoded slash/path, trailing slash, duplicate slash, or alternate escaped spelling before backend invocation.
- Server authorization requires exactly one `Authorization` header field containing exactly one value equal to `Bearer <token>`. Duplicate valid, duplicate valid-plus-invalid, comma-joined, alternate scheme/case, and alternate-whitespace forms are rejected before constant-time token comparison and before backend invocation.
- IPC credentials appear only in the controlled fzf/callback environment, never in action text, command arguments, files, stderr, logs, traces, preview output, renderer/zoxide/probe/noncallback environments, cache entries, or temporary artifacts.
- Every `sessionipc.Backend` method must promptly honor its request context and return after cancellation; the production actor backend satisfies this interface precondition. Under that supported contract, shutdown is bounded: reject new work, cancel the server base and handler contexts, close the listener, perform bounded `http.Server.Shutdown`/`Close`, wait for the handler `WaitGroup`, return, and only then close the actor. No backend call is detached into an untracked goroutine. A backend that ignores cancellation violates the interface precondition; Go cannot forcibly stop it without leaking a goroutine, and no reclaim guarantee or test is made for that unsupported backend.
- Every fzf and child environment removes `FZF_DEFAULT_OPTS`, `FZF_DEFAULT_OPTS_FILE`, `FZF_DEFAULT_COMMAND`, and stale `SHELL_PICKER_*`; Windows matching is case-insensitive and deduplicates keys. fzf callback address/token are dedicated controlled config fields added only after inherited sanitization.
- Preview callbacks validate the exact current record through IPC and require its authoritative target to be a filesystem location before running preview detection and tools in the callback process. `KindVirtual` is never resolved as a preview path. fzf owns each Unix callback process group and each Windows callback tree, and therefore owns preview lifetime.
- fzf keeps only the latest preview and requests cancellation of the previous callback. Steady state permits one preview callback/tree; replacement may briefly permit two only until the old complete tree exits within the external bound measured by the terminal harness.
- `process.Spec` keeps `Stdin io.Reader`, `Stdout io.Writer`, and `Stderr io.Writer`. Direct `*os.File` streams remain caller-owned, are attached directly when applicable, and are never closed by `Runner`. Non-file streams use pumps and are supported only when finite/nonblocking/cooperative, or when they implement `io.Closer` whose `Close` is nonblocking and promptly unblocks every pending `Read`/Write`; a pumped closer used for emergency unblocking must have stable reference identity, normally a non-nil pointer implementing `io.Closer`. Non-pointer/value closers, typed-nil pointers, and any other closer without stable identity are rejected with a clear stream-validation error before `attempt`, executable lookup, or process launch; they are never structurally compared or treated as supported cooperative streams. Passing a valid pumped closer explicitly grants `Runner` permission to call `Close` only on cancellation or `WaitDelay` emergency cleanup, never on ordinary completion. The same pointer closer shared across stdin/stdout/stderr is registered once and emergency-closed at most once. Universal bounded cleanup is claimed only for this supported stream contract. Arbitrary nonclosable user stream implementations that block forever violate the API precondition and are outside the resource guarantee because Go cannot forcibly stop their blocked methods. For an already-started `ContainmentInheritTree` child, `Child.RetainTree` may create one explicit scoped `TreeHandle` before `Wait`; `Wait` still reaps the child and closes its own resources, while the retained handle supports idempotent post-Wait `KillTree` until idempotent `Close`. Unix retains the inherited callback PGID, which remains authoritative while the callback group leader lives; Windows duplicates the nested Job handle before `Wait`. Retain/Wait/Kill/Close are synchronized, handles are never duplicated implicitly, and callers always close them.
- `process.Spec.ExtraFiles` contains caller-owned non-nil `*os.File` values inherited by Unix children at descriptors `3+i`; Runner never closes them, and Windows rejects nonempty ExtraFiles as unsupported. ForegroundTTY is appended after these descriptors and its child descriptor accounts for their count. Rich artifact renderers use no helper process: on Unix they keep the validated read handle open, pass it through ExtraFiles, and give the existing renderer `/proc/self/fd/N` on Linux or `/dev/fd/N` on other supported Unix; on Windows they keep a validated read handle open with sharing that permits renderer reads but denies write/delete/replacement until the existing renderer exits.
- `process.Spec` selects `ContainmentForegroundTree` for interactive fzf, `ContainmentOwnTree` for zoxide/background children, and `ContainmentInheritTree` for preview renderers. Unix foreground containment appends caller-owned `Spec.ForegroundTTY` to `exec.Cmd.ExtraFiles`, giving the child a known tty descriptor (normally 3 when no explicit ExtraFiles precede it) for child-side use and tests, while setting `SysProcAttr.Ctty` to `int(Spec.ForegroundTTY.Fd())`, the parent descriptor required by Go's `Foreground` API because Go performs `TIOCSPGRP` before fd remapping. The Go distinction is critical: `Foreground` uses a parent fd, whereas `Setctty` uses a child fd. Foreground containment keeps `Setpgid:true`, `Foreground:true`, and Linux `Pdeathsig`, atomically gives fzf's new process group the controlling terminal, preserves caller ownership/lifetime, and restores the prior foreground group after exit. Restoration locks the current OS thread, uses `unix.PthreadSigmask` (or the supported `x/sys/unix` equivalent) to save the exact prior thread mask and block `SIGTTOU`, performs bounded EINTR-aware `TIOCSPGRP`, restores the exact saved mask, and then unlocks the thread; it never calls process-global `signal.Ignore` or `signal.Reset` and never mutates the caller's signal disposition. Windows maps foreground containment to the owned Job/console process. Unix internal timeout/output cancellation of a started external renderer kills the entire inherited callback group, intentionally terminating that callback, so it cannot promise fallback or final telemetry; Windows preview renderers use a nested non-inheritable kill-on-close Job Object.
- Preview categories include eza directory listing; glow, bat, and internal text; kitten, chafa, and image metadata; pdftoppm PDF; ffmpegthumbnailer video; ffmpeg and exiftool audio; zip, gzip, xz, tar, and bzip listings; every category has a safe native fallback.
- Preview requests require an authoritative filesystem target with an absolute path, reject `KindVirtual`, run under a 10-second deadline, emit at most 4 MiB through a counting writer, bound internal input/line counts, allow at most one simultaneously live converter/renderer direct child per callback, and allow at most three sequential direct child starts per callback; converter plus kitten plus chafa is the maximum chain, while native fallback starts none. Native/fake limits return errors; a started external renderer limit cancels its whole inherited callback group/tree.
- Archive work is limited to 100 entries, 4 MiB decompressed data, and 10 seconds; 64 MiB is the maximum artifact accepted into or returned from the cache.
- Preview cache keys are SHA-256 over length-delimited absolute path bytes, size, nanosecond mtime, and renderer identity. Every cache operation freshly walks and opens root components without following symlinks/reparse points, anchors all entry/temp/publication/timestamp/prune operations to the validated directory handle, and validates object identity through opened no-follow handles. Unix uses `openat`/`mkdirat`/`linkat`/`unlinkat`/`fstatat`/`utimensat`-equivalent operations; Windows uses handle-relative NT/file-information APIs with reparse checks and file IDs. Independent absolute-path check-then-use is not an accepted cache security boundary. Entries/winners must be regular, non-reparse, at most 64 MiB, have one final link, and retain the same inode/file ID across publication/open/acceptance. Exclusive 0600 same-directory temporary writes use atomic no-replace publication rather than overwrite-capable final rename. Cross-process races accept only one validated immutable winner, and 512 MiB pruning is best effort with saturating accounting. Converter-size polling is best-effort transient disk control, not a hard transient disk cap; final opened-handle regular-file, identity, link-count, and 64 MiB validation is authoritative.
- Zsh preserves the exact `cd ` Space trigger, the `cp` Tab parser and ordinary-completion fallback, Zsh quoting, immediate builtin `cd`, and `cp` insertion without a trailing space.
- Nushell preserves ordinary Space unless the buffer at the cursor is exactly `cd` or `cp`, preinserts that trigger Space so abort/error leaves `cd ` or `cp `, uses `%cd` for environment mutation, inserts each accepted `cp` path as safe NUON without a trailing space, and leaves existing Tab completion unchanged.
- On Windows `/` opens a virtual Drives location. At a drive root or UNC share root, local enumeration emits ordinary filesystem `.` followed by a valid `KindVirtual` `..` record with the fixed canonical `drives` payload and authoritative `pathutil.Drives()` target, then children; forward navigation and Enter on that virtual record open Drives rather than accepting it. At non-root filesystem locations, `..` remains an ordinary filesystem parent; Drives itself continues to emit only drive records and no dot records. Home navigates normally; Add in Drives displays `[A!]` and changes no state.
- UNC and zoxide paths are navigable; `cp` output falls back to an absolute path when `filepath.Rel` cannot cross volumes.
- Add reduction performs only lexical validation and pure calculations; it performs no `Lstat`, mkdir, rollback, filesystem call, actor call, goroutine start, or mutation. `Handle` validates every existing component of the complete absolute base ancestry from filesystem root or UNC share root through the base, then every query component, through its single `CreateDirectoryTree` call; that operation uses `Lstat` and native reparse checks, rejects any symlink/reparse point, creates missing directories incrementally, and cleans up partial-create failures. `Handle` rolls back any successfully returned tree if proposal preparation fails before `Actor.Apply`; after `Actor.Apply` receives the complete proposal, the actor exclusively owns keep/reverse-rollback ordering and waits for generation completion before rollback. Concurrent namespace replacement by another process between checks and create/use remains the documented unavoidable TOCTOU boundary.
- Windows prompts preserve native separator backslashes as one visible separator (`C:\`, `Drives\`); Unix prompts escape filename backslashes. Typed action constructors separately honor fzf grammar. Because fzf 0.74.1 does not unescape backslash-prefixed delimiters, dynamic `change-prompt` is always the terminal colon-delimited action in a sequence; this preserves arbitrary prompt text literally and prevents it from becoming a second action. Prompt/action constructors reject CR, LF, and NUL and are fuzzed against fzf delimiter/action-injection corpora; full candidate identity remains the authorization boundary.
- Preview authorization means exact current-snapshot full-record membership at resolve time plus an authoritative filesystem path; `KindVirtual` membership authorizes navigation only, never preview or output. Preview DTOs do not claim generation freshness or immutable filesystem-object identity. Native candidate semantics intentionally follow symlinks, and external renderers consume paths; replacement or retargeting after authenticated resolution is the documented unavoidable same-user filesystem TOCTOU boundary, not a reason to reject symlinks, copy artifacts, or add no-follow descriptor semantics.
- Stable CI gates pure codec/action/merge allocations, reports rather than gates OS enumeration allocations, and enforces behavior, process budgets, handler bounds, and Linux race checks rather than wall time.
- Dedicated performance goals remain warm startup p95 at most 150 ms Linux and 275 ms Windows, navigation p95 at most 90 ms Linux and 180 ms Windows, and preview dispatch p95 at most 20 ms Linux and 50 ms Windows; enforcement starts only after recording host/CPU/power/terminal/antivirus metadata and establishing a host baseline around a prebuilt binary.
- Process budgets are one event callback per transform, one steady-state preview callback/tree, at most two preview callback/trees only during focus replacement until the old tree exits within the bound, at most one simultaneously live converter/renderer direct child and at most three sequential direct child starts per callback, one fzf, and at most 16 concurrent IPC handlers. Zoxide budgets distinguish invocation from OS creation: cached `cd` makes exactly one attempt per session, zero or one successful start, at most one live process, and no later attempt; fresh `cd` makes exactly one attempt and zero or one successful start per generation with at most one live process in that picker session, while independent sessions may query concurrently; `cp` makes zero attempts and zero starts.
- Probe output and trace JSONL redact every path as `sha256:` followed by the first 16 lowercase hash characters and never contain bearer tokens, query text, raw records, or raw paths.
- Production Go files remain at or below 350 lines and test files at or below 500 lines; split only at the responsibilities named in the file map and do not add repository, service, event-bus, or dependency-injection frameworks.
- Every `*_unix.go` and `*_unix_test.go` file begins with `//go:build !windows`; every Windows counterpart/test begins with `//go:build windows`, and cross-build CI compiles all tag partitions.
- GitHub Actions references are supported pinned majors: `actions/checkout@v5`, `actions/setup-go@v6`, `actions/upload-artifact@v4`, and `actions/download-artifact@v4`.
- Every task follows red/green TDD, runs the exact focused command shown, then runs the stated package or integration gate before its conventional commit.

---

## File Map

| Path | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Pin the module, Go/toolchain versions, and sole `golang.org/x/sys v0.47.0` dependency. |
| `.gitignore` | Ignore built binaries, coverage, traces, benchmark output, and local preview caches. |
| `Makefile` | Expose format, unit, race, integration, real-fzf, cross-build, performance, and release checks. |
| `cmd/shell-picker/main.go`, `signals_unix.go`, `signals_windows.go` | Inject the build version, derive a platform signal context, and delegate process exit to `internal/app`. |
| `internal/protocol/types.go` | Define picker, mode, opcode, cursor, event, effect, resolved candidate, and outcome contracts. |
| `internal/protocol/codec.go` | Implement padded base64, byte-safe display escaping, strict record parsing, and NUL framing. |
| `internal/protocol/outcome.go` | Encode NUL outcomes for Zsh and JSON-subset NUON outcomes for Nushell. |
| `internal/pathutil/location.go` | Define filesystem and virtual-Drives locations plus common parent, relative, and Add validation behavior. |
| `internal/pathutil/location_unix.go`, `location_windows.go` | Implement POSIX root and Windows drive/UNC semantics with native APIs. |
| `internal/pathutil/add_unix.go`, `add_windows.go` | Safely create Add trees without symlink/reparse traversal and roll back new empty directories. |
| `internal/process/process.go` | Define direct child-process start, containment mode, wait, and tree-cancellation contracts. |
| `internal/process/env.go` | Sanitize inherited environments with Windows case-insensitive deduplication. |
| `internal/process/process_unix.go`, `process_linux.go`, `process_kqueue_freebsd.go`, `process_unsupported_unix.go` | Implement common inherited/owned Unix process trees, Linux details, the validated FreeBSD kqueue non-reaping exit observer, unsupported-Unix rejection, foreground terminal transfer/restoration with thread-local SIGTTOU masking, cooperative pumps, and group cancellation. |
| `internal/process/process_windows.go`, `handles_windows.go`, `job_windows.go` | Implement low-level suspended Windows launches, explicit inherited-handle lists, cooperative pumps, and Job lifecycle without `exec.Cmd.Start`. |
| `internal/candidate/record.go` | Bind wire records to authoritative filesystem-or-Drives targets and snapshot-safe full-record keys without interpreting virtual tokens as paths. |
| `internal/candidate/local.go` | Batch local enumeration, category ordering, and bounded symlink classification. |
| `internal/candidate/local_unix.go`, `local_windows.go` | Enumerate POSIX roots and the Windows virtual Drives view. |
| `internal/candidate/zoxide.go` | Load one immutable zoxide cache under the configured budget and transactionally discard partial output. |
| `internal/candidate/builder.go` | Apply cached or fresh policy, own the session-scoped cancellation-aware fresh-query permit, overlap the required local/cache work, and merge local-first immutable results. |
| `internal/session/types.go` | Define complete proposed state, immutable snapshots, cloned Add intents, exclusive pure reductions, pending transition/effect templates, and validation errors. |
| `internal/session/actor.go` | Serialize commands, keep reads live during pending generation, and atomically publish complete transitions. |
| `internal/session/events.go` | Purely reduce mode, slash, tilde, forward, parent, Enter, and Add events, then resolve Add filesystem intent in standalone `Handle` before one actor apply. |
| `internal/session/selection.go` | Validate accepted full records and restore snapshot order and multiplicity. |
| `internal/sessionipc/dto.go` | Define exact JSON request/response DTOs with base64 byte fields. |
| `internal/sessionipc/auth.go` | Generate and constant-time validate the single 256-bit bearer token. |
| `internal/sessionipc/server.go` | Serve only canonical `/v1/event`, `/v1/load`, and `/v1/preview` targets with exact Authorization grammar, bounded handlers, cooperative backend cancellation, and ordered shutdown. |
| `internal/sessionipc/client.go` | Strictly parse the loopback endpoint and send bearer-authenticated bounded requests while enforcing per-class response limits and body lifecycle. |
| `internal/fzf/action.go` | Render only typed fzf actions and escape dynamic prompt arguments. |
| `internal/fzf/options.go` | Build exact common, `cd`, and `cp` fzf options and static bindings. |
| `internal/fzf/process.go` | Launch one fzf with basename `--with-shell`, explicit controlled callback credentials, sanitized inherited environment, and direct streams. |
| `internal/fzf/output.go` | Parse NUL-framed fzf acceptance output and abort exit codes. |
| `internal/callback/grammar.go` | Parse the three-command callback grammar and reject all other text. |
| `internal/callback/dispatch.go` | Read fzf environment, call IPC, emit actions/snapshots, and execute bounded resolved previews. |
| `internal/callback/cursor_unix.go`, `cursor_windows.go` | Apply line/block cursor shape without contaminating callback stdout. |
| `internal/app/cli.go` | Parse public picker/probe commands, `--zoxide-timeout`, `--zoxide-policy`, hidden callback mode, output format, and tracing. |
| `internal/app/run.go` | Configure the cached session cache or fresh generation-cache factory, create the actor and IPC server, launch one fzf, validate output, and close the session. |
| `internal/app/tty_unix.go`, `tty_windows.go` | Open the controlling terminal used for fzf foreground ownership and close it with the session. |
| `internal/app/version.go` | Hold version reporting and release-injected build metadata. |
| `internal/preview/detect.go` | Detect directory, text, image, PDF, video, audio, archive, and binary categories. |
| `internal/preview/render.go` | Select direct tools/native fallbacks under deadline, output, and process bounds. |
| `internal/preview/limit.go` | Enforce 10-second/4 MiB/bounded-input limits and the 64 MiB accepted/cached artifact maximum. |
| `internal/preview/cache.go`, `cache_unix.go`, `cache_windows.go` | Compute cache keys, publish render products atomically without replacement on each OS, and prune to 512 MiB. |
| `internal/preview/archive.go` | Implement bounded native zip, gzip, and tar listings plus xz/bzip fallbacks. |
| `internal/integration/probe.go` | Report fzf, zoxide, preview-tool, cache, terminal, and adapter readiness. |
| `internal/integration/trace.go` | Write path-redacted JSONL lifecycle and process-count events. |
| `internal/integration/benchmark.go` | Measure startup variants, cached and fresh navigation, and preview dispatch p95 scenarios and emit machine-readable results. |
| `adapters/zsh/shell-picker.plugin.zsh` | Provide exact ZLE Space and Tab widgets and byte-safe NUL output handling. |
| `adapters/zsh/shell-picker.plugin.test.zsh` | Test triggers, parser fallback, quoting, immediate cd, abort, and no trailing space. |
| `adapters/nushell/shell-picker.nu` | Provide Space widgets, `%cd`, NUON insertion, and unchanged Tab behavior. |
| `adapters/nushell/shell-picker.test.nu` | Test trigger exactness, fallback Space, environment mutation, and insertion safety. |
| `integration/testdata/parity/source-assertions.json` | Store one row for each of the 371 authoritative generated assertions. |
| `integration/testdata/parity/golden/` | Store checked-in NUL and JSON golden outputs keyed by parity row IDs. |
| `integration/parity_manifest_test.go` | Enforce source hashes, suite counts, unique IDs, runners, and total coverage. |
| `integration/parity_linux_test.go` | Execute Linux codec, candidate, navigation, mode, Add, preview, and adapter parity rows. |
| `integration/parity_windows_test.go` | Execute the same semantic rows with Windows root/path substitutions. |
| `integration/fzf_real_test.go`, `fzf_real_linux_test.go`, `fzf_real_windows_test.go`, `fzf_real_pipe_test.go` | Drive fzf 0.74.1 through PTY/ConPTY, verify exact pipe NUL I/O, callbacks, resize, replacement cancellation, and one-process sessions. |
| `integration/testhelper/main.go` | Provide the blocking renderer and renderer-grandchild process used by production subprocess cancellation tests. |
| `integration/security_failure_test.go` | Exercise forged identities, bearer failures, limits, stale work, endpoint closure, and leaks. |
| `integration/performance_test.go` | Gate allocations/process counts and run dedicated wall-time targets only when opted in. |
| `integration/source_limits_test.go` | Enforce deterministic 350-line production and 500-line test limits across checked-in Go sources. |
| `.github/workflows/ci.yml` | Run Linux/Windows unit, race, adapter, parity, allocation, process-count, and cross-build gates. |
| `.github/workflows/real-fzf.yml` | Run opt-in and scheduled real-fzf 0.74.1+ Linux/Windows matrices. |
| `.github/workflows/performance.yml` | Run dedicated self-hosted p95 targets without burdening shared CI. |
| `.github/workflows/release.yml` | Build four platform archives, checksum them, and publish tagged releases. |
| `README.md`, `LICENSE` | Document installation, use, dependencies, support, and MIT licensing. |
| `docs/architecture.md`, `docs/protocol.md`, `docs/adapters.md` | Document actor/IPC flow, exact callback wire contracts, and shell setup. |
| `docs/preview.md`, `docs/performance.md`, `docs/parity.md`, `docs/security.md` | Document renderers/cache, targets/tracing, source coverage, and trust boundaries. |

### Task 1: Bootstrap the Module, Version Command, and Quality Targets

**Files:**
- Create: `go.mod`
- Create: `go.sum`
- Create: `.gitignore`
- Create: `Makefile`
- Create: `cmd/shell-picker/main.go`
- Create: `internal/app/cli.go`
- Create: `internal/app/version.go`
- Create: `internal/app/cli_test.go`

**Interfaces:**
- Consumes: process arguments and standard streams only.
- Produces: `app.Streams`, `app.Main(context.Context, []string, app.Streams, string) int`, and `app.Version(string) string`.

- [ ] **Step 1: Write the failing CLI test before the module exists**

```go
package app

import (
  "bytes"
  "context"
  "testing"
)

func TestVersionCommand(t *testing.T) {
  var stdout, stderr bytes.Buffer
  code := Main(context.Background(), []string{"version"}, Streams{Out: &stdout, Err: &stderr}, "v1.2.3")
  if code != 0 || stdout.String() != "shell-picker v1.2.3\n" || stderr.Len() != 0 {
    t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
  }
}
```

- [ ] **Step 2: Run the test to prove the repository is not bootstrapped**

Run: `go test ./internal/app -run TestVersionCommand -count=1`

Expected: FAIL with `go: cannot find main module`.

- [ ] **Step 3: Add the pinned module and minimal complete CLI**

```go
module github.com/AntoineGS/shell-picker

go 1.26.0

toolchain go1.26.5

require golang.org/x/sys v0.47.0
```

```go
package app

type Streams struct {
  Out io.Writer
  Err io.Writer
}

func Version(build string) string {
  if build == "" {
    return "dev"
  }
  return build
}

func Main(_ context.Context, args []string, streams Streams, build string) int {
  if len(args) == 1 && args[0] == "version" {
    fmt.Fprintf(streams.Out, "shell-picker %s\n", Version(build))
    return 0
  }
  fmt.Fprintln(streams.Err, "usage: shell-picker version")
  return 2
}
```

Set `cmd/shell-picker/main.go` to pass `os.Args[1:]`, `os.Stdout`, and `os.Stderr` to `app.Main`; declare `var version = "dev"` for `-ldflags` injection. Set `.gitignore` entries to `/bin/`, `/dist/`, `/coverage/`, `*.trace.jsonl`, `*.bench.json`, and `.shell-picker-cache/`. The initial Makefile contains `fmt` as `gofmt -w cmd internal`, `test` as `go test ./... -count=1`, and `check` depending on `test`; Tasks 19, 21, 22, and 24 extend it when their named files and gates exist.

- [ ] **Step 4: Resolve the sole module and verify the command**

Run: `go mod download golang.org/x/sys@v0.47.0 && go test ./internal/app -run TestVersionCommand -count=1 && go run ./cmd/shell-picker version`

Expected: PASS followed by `shell-picker dev`; `go.sum` contains only `golang.org/x/sys v0.47.0` module hashes.

- [ ] **Step 5: Commit the bootstrap**

```bash
git add go.mod go.sum .gitignore Makefile cmd/shell-picker/main.go internal/app/cli.go internal/app/version.go internal/app/cli_test.go
git commit -m "chore(core): bootstrap Go module"
```

### Task 2: Add Byte-Safe Protocol Codecs and Outcome Formats

**Files:**
- Create: `internal/protocol/types.go`
- Create: `internal/protocol/codec.go`
- Create: `internal/protocol/outcome.go`
- Create: `internal/protocol/codec_test.go`
- Create: `internal/protocol/outcome_test.go`

**Interfaces:**
- Consumes: arbitrary path bytes, candidate wire bytes, and accepted path byte slices.
- Produces: `protocol.Picker`, `protocol.Mode`, `protocol.Kind` including `protocol.KindVirtual`, fixed `protocol.VirtualDrivesTarget`, `protocol.Opcode`, `protocol.Event`, `protocol.Effect`, `protocol.EncodePath([]byte) string`, `protocol.DecodePath(string) ([]byte, error)`, `protocol.EscapeDisplay([]byte) string`, `protocol.ParseRecord([]byte) (protocol.WireRecord, error)`, `protocol.FrameRecords([]protocol.WireRecord) []byte`, `protocol.WriteFramedRecords(io.Writer, []protocol.WireRecord) error`, and `protocol.EncodeOutcome(io.Writer, protocol.OutputFormat, protocol.Outcome) error`.

- [ ] **Step 1: Write table tests for all authoritative byte cases and strict framing**

```go
func TestPathCodecAndDisplay(t *testing.T) {
  cases := []struct{ name string; raw []byte; display string }{
    {"tab", []byte("tab\tname"), `tab\tname`},
    {"newline", []byte("line\nname"), `line\nname`},
    {"trailing-space", []byte("trailing "), "trailing "},
    {"backslash", []byte(`back\slash`), `back\\slash`},
    {"nbsp", []byte("nbsp\u00a0name"), "nbsp\u00a0name"},
    {"leading-dash", []byte("-leading"), "-leading"},
    {"control", []byte{'c', 1, 'x'}, `c\x01x`},
    {"ending-newline", []byte("ending-newline\n"), `ending-newline\n`},
    {"apostrophe", []byte("apostrophe'path"), `apostrophe\'path`},
    {"invalid-utf8", []byte{'x', 0xff}, `x\xFF`},
  }
  for _, tc := range cases {
    payload := EncodePath(tc.raw)
    got, err := DecodePath(payload)
    if err != nil || !bytes.Equal(got, tc.raw) || EscapeDisplay(tc.raw) != tc.display {
      t.Fatalf("%s payload=%q got=%q display=%q err=%v", tc.name, payload, got, EscapeDisplay(tc.raw), err)
    }
  }
  for _, bad := range []string{"", "not%base64", "YQ", "YQ==junk"} {
    if _, err := DecodePath(bad); err == nil { t.Fatalf("accepted %q", bad) }
  }
}

func TestRecordRequiresExactlyTwoTabsAndNULFrames(t *testing.T) {
  record := WireRecord{Kind: KindFile, Display: `a\tb`, Payload: EncodePath([]byte("a\tb"))}
  framed := FrameRecords([]WireRecord{record})
  if bytes.Count(record.Bytes(), []byte{'\t'}) != 2 || framed[len(framed)-1] != 0 { t.Fatalf("record=%q framed=%q", record.Bytes(), framed) }
  for _, bad := range [][]byte{[]byte("file\tone"), []byte("file\tone\ttwo\tthree"), []byte("bad\x00x\tp\tq")} {
    if _, err := ParseRecord(bad); err == nil { t.Fatalf("accepted %q", bad) }
  }
}

func TestVirtualKindUsesCanonicalNonemptyDrivesToken(t *testing.T) {
  payload := EncodePath([]byte(VirtualDrivesTarget))
  record := WireRecord{Kind: KindVirtual, Display: "..", Payload: payload}
  parsed, err := ParseRecord(record.Bytes())
  if err != nil { t.Fatal(err) }
  decoded, err := DecodePath(parsed.Payload)
  if err != nil || !bytes.Equal(decoded, []byte("drives")) || payload != "ZHJpdmVz" {
    t.Fatalf("payload=%q decoded=%q err=%v", payload, decoded, err)
  }
  for _, bad := range []string{"", "ZHJpdmVz=", "ZHJpdmVz==", "b3RoZXI="} {
    raw := WireRecord{Kind: KindVirtual, Display: "..", Payload: bad}.Bytes()
    if _, err := ParseRecord(raw); err == nil { t.Fatalf("accepted virtual payload %q", bad) }
  }
}
```

- [ ] **Step 2: Run protocol tests to verify missing symbols**

Run: `go test ./internal/protocol -run 'TestPathCodecAndDisplay|TestRecordRequiresExactlyTwoTabsAndNULFrames|TestVirtualKindUsesCanonicalNonemptyDrivesToken' -count=1`

Expected: FAIL with `undefined: EncodePath` and `undefined: WireRecord`.

- [ ] **Step 3: Define protocol enums, effects, strict codecs, and outputs**

```go
type Picker string
const (PickerCD Picker = "cd"; PickerCP Picker = "cp")
type Mode string
const (ModeInsert Mode = "insert"; ModeNormal Mode = "normal"; ModeAdd Mode = "add")
type Kind string
const (KindLocal Kind = "local"; KindDirectory Kind = "directory"; KindFile Kind = "file"; KindZoxide Kind = "zoxide"; KindDrive Kind = "drive"; KindVirtual Kind = "virtual")
const VirtualDrivesTarget = "drives"
type CursorShape string
const (CursorLine CursorShape = "line"; CursorBlock CursorShape = "block")
type Opcode string
const (
  OpModeInsert Opcode = "mi"; OpModeAdd Opcode = "ma"; OpEscape Opcode = "es"; OpForward Opcode = "fw"
  OpParent Opcode = "up"; OpSlash Opcode = "sl"; OpHome Opcode = "hm"; OpEnter Opcode = "en"
)
type Event struct { Opcode Opcode; Key string; Query, CurrentItem []byte }
type Effect struct {
  Mode Mode `json:"mode"`; Prompt string `json:"prompt"`; Search string `json:"search"`; Rebind Mode `json:"rebind"`
  ClearQuery bool `json:"clear_query"`; ClearMulti bool `json:"clear_multi"`; Accept bool `json:"accept"`
  Ignore bool `json:"ignore"`; Put string `json:"put"`; ReloadGeneration uint64 `json:"reload_generation"`
  Cursor CursorShape `json:"cursor"`; ErrorPrompt bool `json:"error_prompt"`
}
type OutputFormat string
const (OutputNUL OutputFormat = "nul"; OutputNUON OutputFormat = "nuon")
type Status string
const (StatusAccepted Status = "accepted"; StatusAborted Status = "aborted")
type Outcome struct { Status Status; Paths [][]byte }
type ResolvedCandidate struct { Kind Kind; Path []byte; Size int64; ModTimeUnixNano int64; Mode uint32 }
```

Use `base64.StdEncoding.EncodeToString` and `base64.StdEncoding.Strict().DecodeString`; reject an empty payload and require re-encoding the decoded bytes to reproduce the payload exactly. `EscapeDisplay` leaves valid printable UTF-8 and spaces unchanged, emits `\\`, `\'`, `\t`, `\n`, `\r`, and uppercase `\xNN` for controls, DEL, and invalid UTF-8 bytes. `ParseRecord` rejects NUL, a kind outside the closed set `local|directory|file|zoxide|drive|virtual`, empty payload, non-canonical base64, and any tab count other than two. `VirtualDrivesTarget` is the fixed string `"drives"`; its exact raw token bytes are `[]byte("drives")`, and its normal canonical padded-base64 wire payload is exactly `ZHJpdmVz`. `ParseRecord` accepts `KindVirtual` only when its decoded payload equals that fixed token and the wire spelling is exactly canonical; it rejects even a different nonempty canonical token such as `b3RoZXI=`. This virtual-only restriction does not reserve or reinterpret arbitrary decoded path bytes for any filesystem kind. `FrameRecords` appends one NUL after every record and no newline. `WriteFramedRecords` loops over exact record bytes plus one NUL with `writeAll`, returning the first short-write or writer error; IPC loads and initial fzf input use this error-propagating path.

NUL output writes each accepted path followed by NUL and writes nothing for abort. NUON output uses `encoding/json` because JSON is a safe NUON subset and emits the exact object shape `{"status":"accepted","paths":["path"]}` with the actual accepted strings, or `{"status":"aborted","paths":[]}`, followed by newline; reject `OutputNUON` when a path is not valid UTF-8.

- [ ] **Step 4: Verify protocol and allocation behavior**

Run: `go test ./internal/protocol -count=1 && go test ./internal/protocol -run '^$' -bench 'Benchmark(FrameRecords|EscapeDisplay)$' -benchmem`

Expected: PASS; benchmarks report no subprocesses and `FrameRecords` uses at most two allocations for a pre-sized record slice.

- [ ] **Step 5: Commit the protocol**

```bash
git add internal/protocol
git commit -m "feat(protocol): add byte-safe picker wire formats"
```

### Task 3: Freeze the 371-Assertion Source Parity Matrix

**Files:**
- Create: `integration/testdata/parity/source-assertions.json`
- Create: `integration/parity_manifest_test.go`
- Create: `integration/testdata/parity/golden/codec-records.bin`
- Create: `integration/testdata/parity/golden/batch-encoder.bin`
- Create: `integration/testdata/parity/golden/cd-order.bin`
- Create: `integration/testdata/parity/golden/cp-order.bin`

**Interfaces:**
- Consumes: the five pinned authoritative source hashes in Global Constraints.
- Produces: one immutable row per generated assertion with fields `id`, `suite`, `source_line`, `runner`, `case`, `check`, `expected_text`, and optional `golden`; `loadParityMatrix(t *testing.T) []parityRow` is available to later integration tests.

- [ ] **Step 1: Write the manifest coverage test before the matrix exists**

```go
type parityRow struct {
  ID string `json:"id"`
  Suite string `json:"suite"`
  SourceLine int `json:"source_line"`
  Runner string `json:"runner"`
  Case string `json:"case"`
  Check string `json:"check"`
  ExpectedText string `json:"expected_text"`
  Golden string `json:"golden,omitempty"`
}

func loadParityMatrix(t *testing.T) []parityRow {
  t.Helper()
  raw, err := os.ReadFile("testdata/parity/source-assertions.json")
  if err != nil { t.Fatal(err) }
  var manifest struct { Assertions []parityRow `json:"assertions"` }
  if err := json.Unmarshal(raw, &manifest); err != nil { t.Fatal(err) }
  return manifest.Assertions
}

func TestParityManifestCoverage(t *testing.T) {
  rows := loadParityMatrix(t)
  want := map[string]int{
    "codec": 50, "batch-encoder": 2, "directory-enumeration": 72, "cd-merged": 8,
    "operations": 34, "slash": 28, "modal": 20, "create": 51, "preview": 3,
    "zshrc-cd": 42, "zshrc-cp": 43, "zshrc-add-mode-query-bindings": 6,
    "zshrc-add-mode-navigation-bindings": 12,
  }
  got := map[string]int{}
  ids := map[string]bool{}
  for _, row := range rows {
    got[row.Suite]++
    if ids[row.ID] || row.Runner == "" || row.Check == "" { t.Fatalf("invalid row: %+v", row) }
    ids[row.ID] = true
  }
  if !reflect.DeepEqual(got, want) || len(rows) != 371 { t.Fatalf("counts=%v total=%d", got, len(rows)) }
}
```

- [ ] **Step 2: Run the coverage test to verify the fixture is absent**

Run: `go test ./integration -run TestParityManifestCoverage -count=1`

Expected: FAIL with `open testdata/parity/source-assertions.json: no such file or directory`; the loader is defined in the test, so this is a fixture absence failure rather than an undefined-symbol failure.

- [ ] **Step 3: Transcribe the exact dynamic assertion expansion into the matrix**

Use IDs `SRC-001` through `SRC-371` in source execution order and the following fixed expansion; each comma-separated check becomes its own row, and each named case repeats those checks exactly:

```json
{
  "sources": {
    "fzf-picker-candidates.zsh": "5300b66b7815e8b1c2f75f230033a069a1c305600faea164c9214cd52e07cb97",
    "fzf-preview.sh": "232eb46eef32bff642985e42edbf0cd3a49098e7485eb6f5b0db0bdf48024159",
    "fzf-batch-encode.pl": "055d9a74cce513bbf02475fae97154b159de98d51cc565cf4859ced0226878fd",
    "fzf-picker.test.zsh": "f920b8f6194c76d5f8a1737c6e4860ab04f641da291dc3e984cbb63443552776",
    ".zshrc": "3bc868023693945a97b2e23f8f806ae5bdaa228a9898fb86cd8ad075e559ab18"
  },
  "assertions": [
    {
      "id": "SRC-001",
      "suite": "codec",
      "source_line": 87,
      "runner": "codec-roundtrip",
      "case": "tab-name",
      "check": "decoded-bytes-equal-input",
      "expected_text": "true"
    }
  ]
}
```

The checked-in file extends the `assertions` array through `SRC-371`; it does not duplicate the example object outside that array.

| Suite | Exact expansion | Rows |
|---|---|---:|
| `codec` | 9 path byte round trips; 11 records with exactly two tabs; candidate count; 9 kinds; control display excludes octal; 9 escaped displays; 9 payload byte round trips; malformed base64 rejection | 50 |
| `batch-encoder` | NUL golden equality for plain, tab, backslash, control, trailing space, UTF-8, and invalid byte lines; dependency failure propagation | 2 |
| `directory-enumeration` | `cd-local` and `cp`: 10 record tab checks, count, then kind/display/payload for 8 unusual directory names; ignored `.worktrees` kind and display | 72 |
| `cd-merged` | local-first path order; kind order; display order; complete discard after zoxide exit failure, zoxide partial failure, encoder failure, encoder partial failure, and missing encoder | 8 |
| `operations` | parent bytes; navigate directory/source/prompt for Normal, Add, Insert; candidate directory; merged reload and no toggle-sort; zoxide inclusion; three relative outputs; malformed rollback of four state values; two enumeration errors; enumeration rollback of five values; replacement rollback of five values; preparation rollback of four values; preview path bytes; invalid picker rejection and no invocation | 34 |
| `slash` | For `cd` and `cp`: exact `..` parent state/reload/query clear, root clamp/query clear, ordinary query put/state/candidates, Add put, Normal nonempty ignore, Normal empty root/query clear; invalid picker action/candidates; missing directory action; missing keymap action | 28 |
| `modal` | Insert mode/search/unbind/rebind/cursor; Insert Esc mode/marks; Normal Esc mode/action; Add mode/search/unbind/query/cursor; Add Esc mode/rebind/query/marks; Insert Enter and Normal Enter acceptance | 20 |
| `create` | successful new directory existence/location/mode/source/prompt/keymap/mark/query/reload; existing directory location/mode; five invalid queries each preserve mode/location/source/candidates, set `[A!]`, refresh prompt, retain query, and avoid reload | 51 |
| `preview` | literal trailing-newline path, literal NBSP path, and fzf-tab second-field extraction | 3 |
| `zshrc-cd` | widget delimiters; encoded cwd/root/home; callback bindings; initial mode; merged source exclusions; field-three occurrence restrictions; navigation routes; preview route; acceptance parsing and count; Zsh quoting; single selection/sort/query; one fzf; EOF-newline target; accept-line | 42 |
| `zshrc-cp` | widget delimiters; encoded base/cwd/root/home; unrestricted multi; callback bindings; Normal Space; initial mode; navigation count/routes; no direct reload; preview; full-record shape/count map; candidate-order restore; payload decode cardinality; Zsh quoting/join; one fzf; visible order; duplicate multiplicity; unknown full-record rejection | 43 |
| `zshrc-add-mode-query-bindings` | `cd` and `cp` slash delegation; both widgets have no inline slash branch and put tilde before navigation in Add | 6 |
| `zshrc-add-mode-navigation-bindings` | `cd` and `cp` initialize mode, bind Add, bind Escape, have one forward transform and one parent transform; neither retains shift-tab source switching | 12 |

Set `source_line` to the exact assertion call line from the pinned 1,101-line source; loop expansions share that source line but have unique `case` values. Store deterministic picker-protocol output in the four golden files rather than JSON strings. Record the five source hashes in a top-level `sources` object and reject a matrix whose IDs, suite counts, source hashes, or golden SHA-256 values differ. The three source preview rows assert literal trailing-newline/NBSP path handling and fzf-tab second-field extraction; the separate Task 18 category matrix covers renderer selection, bounded fallbacks, and tool failures.

- [ ] **Step 4: Run the manifest test and inspect every row**

Run: `go test ./integration -run TestParityManifestCoverage -count=1`

Expected: PASS with exactly 371 unique rows and the 13 suite counts above.

- [ ] **Step 5: Commit the frozen authority**

```bash
git add integration/parity_manifest_test.go integration/testdata/parity
git commit -m "test(parity): freeze authoritative Linux assertions"
```

### Task 4: Implement POSIX, Windows Drive, UNC, Relative, and Add Path Semantics

**Files:**
- Create: `internal/pathutil/location.go`
- Create: `internal/pathutil/location_unix.go`
- Create: `internal/pathutil/location_windows.go`
- Create: `internal/pathutil/add_unix.go`
- Create: `internal/pathutil/add_windows.go`
- Create: `internal/pathutil/location_unix_test.go`
- Create: `internal/pathutil/location_windows_test.go`

**Interfaces:**
- Consumes: absolute path bytes, operating-system root rules, initial `cwd`, and `home`.
- Produces: `pathutil.Location`, `pathutil.CreatedTree`, `pathutil.Filesystem([]byte) Location`, `pathutil.Drives() Location`, `pathutil.Root() Location`, `pathutil.Parent(Location) Location`, `pathutil.PromptDisplay(Location) string`, `pathutil.Relative([]byte, []byte) []byte`, `pathutil.ValidateAddQuery(Location, []byte) error`, `pathutil.CreateDirectoryTree(Location, []byte) (CreatedTree, error)`, `(*CreatedTree).Rollback() error`, and Windows `pathutil.ListDrives() ([]Location, error)`.

- [ ] **Step 1: Write pure path tests including Windows cross-volume and UNC cases**

```go
//go:build !windows

func TestRelativeAndAddValidation(t *testing.T) {
  if got := string(Relative([]byte("/work"), []byte("/work/-dash"))); got != "./-dash" { t.Fatal(got) }
  if got := string(Relative([]byte("/work"), []byte("/work/a\n"))); got != "a\n" { t.Fatalf("%q", got) }
  if got := PromptDisplay(Filesystem([]byte(`/work/a\b`))); got != `/work/a\\b/` { t.Fatalf("prompt=%q", got) }
  for _, query := range [][]byte{nil, []byte("/absolute"), []byte("../escape"), []byte("one/../escape")} {
    if err := ValidateAddQuery(Filesystem([]byte("/work")), query); err == nil { t.Fatalf("accepted %q", query) }
  }
  if err := ValidateAddQuery(Filesystem([]byte("/work")), []byte("projects/new")); err != nil { t.Fatal(err) }
}

func TestCreateDirectoryTreeRejectsSymlinkAndRollsBack(t *testing.T) {
  root := t.TempDir()
  if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "link")); err != nil { t.Fatal(err) }
  if _, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte("link/child")); !errors.Is(err, ErrUnsafeTraversal) { t.Fatalf("err=%v", err) }
  created, err := CreateDirectoryTree(Filesystem([]byte(root)), []byte("one/two"))
  if err != nil { t.Fatal(err) }
  if err := created.Rollback(); err != nil { t.Fatal(err) }
  if _, err := os.Lstat(filepath.Join(root, "one")); !errors.Is(err, fs.ErrNotExist) { t.Fatalf("rollback err=%v", err) }
}

func TestCreateDirectoryTreeRejectsSymlinkInBaseAncestry(t *testing.T) {
  root := t.TempDir()
  real := filepath.Join(root, "real")
  if err := os.MkdirAll(filepath.Join(real, "base"), 0o755); err != nil { t.Fatal(err) }
  linked := filepath.Join(root, "linked")
  if err := os.Symlink(real, linked); err != nil { t.Fatal(err) }
  if _, err := CreateDirectoryTree(Filesystem([]byte(filepath.Join(linked, "base"))), []byte("child")); !errors.Is(err, ErrUnsafeTraversal) { t.Fatalf("err=%v", err) }
}
```

```go
//go:build windows

func TestWindowsParentModel(t *testing.T) {
  cases := []struct{ in Location; wantKind Kind; want string }{
    {Filesystem([]byte(`C:\`)), KindDrives, ""},
    {Filesystem([]byte(`\\server\share\`)), KindDrives, ""},
    {Filesystem([]byte(`C:\work\child`)), KindFilesystem, `C:\work`},
    {Drives(), KindDrives, ""},
  }
  for _, tc := range cases {
    got := parentWindows(tc.in)
    if got.Kind != tc.wantKind || string(got.Path) != tc.want { t.Fatalf("in=%q got=%+v", tc.in.Path, got) }
  }
  if got := relativeWindows([]byte(`C:\work`), []byte(`D:\data\x`)); string(got) != `D:\data\x` { t.Fatal(string(got)) }
  if got := PromptDisplay(Filesystem([]byte(`C:\`))); got != `C:\` { t.Fatalf("root prompt=%q", got) }
  if got := PromptDisplay(Drives()); got != `Drives\` { t.Fatalf("drives prompt=%q", got) }
}

func TestAbsoluteAncestryWindowsUNC(t *testing.T) {
  got, err := absoluteAncestryWindows(`\\server\share\team\project`)
  if err != nil { t.Fatal(err) }
  want := []string{`\\server\share\`, `\\server\share\team`, `\\server\share\team\project`}
  if !reflect.DeepEqual(got, want) { t.Fatalf("got=%q want=%q", got, want) }
}

func TestCreateDirectoryTreeRejectsJunctionInBaseAncestry(t *testing.T) {
  base := nativeJunctionBaseFixture(t)
  if _, err := CreateDirectoryTree(Filesystem([]byte(base)), []byte(`child`)); !errors.Is(err, ErrUnsafeTraversal) { t.Fatalf("err=%v", err) }
}
```

- [ ] **Step 2: Run tests to verify location APIs are missing**

Run: `go test ./internal/pathutil -count=1`

Expected: FAIL with `undefined: Location` and `undefined: Relative`.

- [ ] **Step 3: Implement lexical byte-preserving paths and native drive discovery**

```go
type Kind uint8
const (KindFilesystem Kind = iota + 1; KindDrives)
type Location struct { Kind Kind; Path []byte }
type CreatedTree struct { Target Location; Created [][]byte }

func ValidateAddQuery(base Location, query []byte) error {
  if base.Kind == KindDrives || len(query) == 0 || isAbsolute(query) { return ErrInvalidAdd }
  for _, part := range splitSeparators(query) {
    if bytes.Equal(part, []byte("..")) { return ErrInvalidAdd }
  }
  return nil
}
```

Put `//go:build !windows` on `location_unix.go`, `add_unix.go`, and `location_unix_test.go`; put `//go:build windows` on all Windows counterparts/tests. On Unix, use `path/filepath` only after converting the unchanged byte slice to a Go string; root parent remains `/`, `filepath.Rel` is lexical, a result beginning `-` gains `./`, and errors return the absolute target bytes. Unix `PromptDisplay` treats backslash as a filename byte and display-escapes it, then appends one `/`. On Windows, normalize only for comparisons, preserve the original absolute spelling for output, recognize `C:\` and `\\server\share\` as roots, map their parent to Drives, map `/` to Drives, and return the absolute target when volume names differ. Windows `PromptDisplay` treats native backslashes as separators, strips duplicate trailing separators, and emits exactly one visible separator so `C:\` and `Drives\` each contain one displayed backslash; typed fzf action escaping remains a later, separate layer. Call `kernel32!GetLogicalDrives` through `syscall.NewLazyDLL` and return existing drives in ascending letter order; do not use `x/sys/windows` in this package. Add the private pure helper `absoluteAncestryWindows(base string) ([]string, error)`, which rejects non-absolute input and returns the exact ordered component candidates from a drive or UNC share root through `base`; its UNC test is lexical only and never stats or contacts the share.

`CreateDirectoryTree` first validates the relative query, then derives the complete absolute base and walks every existing component from the filesystem root through the base before walking every query component. On Windows it consumes `absoluteAncestryWindows` in emitted order, applying `Lstat` and the native reparse check to each emitted path; UNC validation therefore starts at the share root and includes every existing component through the base without making the pure ancestry test touch SMB/network. Unix also uses `Lstat` for each ancestry step and rejects `ModeSymlink`, while Windows calls `GetFileAttributesW` through `syscall` and rejects `FILE_ATTRIBUTE_REPARSE_POINT`. Add is rejected when any existing base ancestor or query component is unsafe, and existing non-directories fail. Missing query components are created one at a time with mode 0777 and appended to `Created` only after successful creation. Any create/check failure removes newly created empty directories in reverse order. `Rollback` performs the same best-effort reverse `os.Remove`, ignores not-exist/nonempty results, clears `Created`, and never removes a pre-existing directory. Tests retain both a Unix symlink in the base ancestry and a real local Windows junction in the base ancestry created with native reparse APIs, plus unsafe query components, a pre-existing file, partial create failure, and preserving pre-existing parents. Concurrent namespace replacement by another process between checks and create/use is documented as the unavoidable TOCTOU boundary.

- [ ] **Step 4: Verify native and cross-compiled path behavior**

Run: `go test ./internal/pathutil -count=1 && GOOS=windows GOARCH=amd64 go test -c ./internal/pathutil -o /tmp/shell-picker-pathutil.test.exe`

Expected: PASS and successful Windows test-binary compilation with no dependency beyond the standard library.

- [ ] **Step 5: Commit path semantics**

```bash
git add internal/pathutil
git commit -m "feat(path): add cross-platform root and relative semantics"
```

### Task 5: Add Direct Child Processes and Tree Cancellation

**Files:**
- Create: `internal/process/process.go`
- Create: `internal/process/env.go`
- Create: `internal/process/process_unix.go`
- Create: `internal/process/process_linux.go`
- Create: `internal/process/process_kqueue_freebsd.go`
- Create: `internal/process/process_unsupported_unix.go`
- Create: `internal/process/process_windows.go`
- Create: `internal/process/handles_windows.go`
- Create: `internal/process/job_windows.go`
- Create: `internal/process/process_test.go`
- Create: `internal/process/process_unix_test.go`
- Create: `internal/process/process_windows_test.go`
- Create: `internal/process/handles_windows_test.go`
- Create: `internal/process/job_windows_test.go`

**Interfaces:**
- Consumes: executable path, argument vector, inherited environment, controlled additions, working directory, streams, containment mode, and cancellation context.
- Produces: `process.Containment`, `process.Spec`, `process.ExitError`, `process.ErrAlreadyWaited`, `process.ErrWaitDelay`, `process.ErrInvalidStream`, `process.ErrExitObserver`, `process.ErrUnsupportedPlatform`, `process.SanitizeEnv([]string, map[string]string) []string`, `process.Runner.Start(context.Context, process.Spec) (*process.Child, error)`, `process.Runner.Run(context.Context, process.Spec) error`, `(*process.Child).Wait() error`, `(*process.Child).KillTree() error`, and `(*process.Child).PID() int`.

- [ ] **Step 1: Write cancellation, cooperative-stream, argument-literal, foreground-TTY descriptor, and process-observation tests**

```go
func TestRunnerPassesArgumentsWithoutShell(t *testing.T) {
  helper := helperCommand(t, "print-args")
  var out bytes.Buffer
  err := (Runner{}).Run(context.Background(), Spec{Path: helper.Path, Args: append(helper.Args, "a b", `$(touch nope)`, `x&y`), Stdout: &out, Containment: ContainmentOwnTree})
  if err != nil || out.String() != "a b\x00$(touch nope)\x00x&y\x00" { t.Fatalf("out=%q err=%v", out.String(), err) }
}

func TestCancelKillsOwnedProcessTreeEventually(t *testing.T) {
  ctx, cancel := context.WithCancel(context.Background())
  child := startHelperTree(t, ctx, ContainmentOwnTree)
  grandchildPID := readPID(t, child)
  cancel()
  if err := child.Wait(); !errors.Is(err, context.Canceled) { t.Fatalf("wait=%v", err) }
  assertProcessGoneWithin(t, grandchildPID, 3*time.Second, 10*time.Millisecond)
}

type foregroundTTYReport struct {
  ParentTTYFD int `json:"parent_tty_fd"`
  ChildTTYFD int `json:"child_tty_fd"`
  SameTTY bool `json:"same_tty"`
  Input string `json:"input"`
  RestoredPreviousGroup bool `json:"restored_previous_group"`
  RestoredThreadMask bool `json:"restored_thread_mask"`
  PreservedSIGTTOUNotification bool `json:"preserved_sigttou_notification"`
  DescriptorDelta int `json:"descriptor_delta"`
  Err string `json:"err,omitempty"`
}

func TestForegroundTreeOwnsTTYAndRestoresPreviousGroup(t *testing.T) {
  baseline := openDescriptorCount(t)
  terminal := startTestPTY(t)
  reportR, reportW := testPipe(t)
  slave := terminal.Slave()
  ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
  helper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestForegroundTTYSessionHelper$")
  helper.Env = append(os.Environ(), "GO_WANT_FOREGROUND_TTY_SESSION=1")
  helper.Stdin, helper.Stdout, helper.Stderr = slave, slave, slave
  helper.ExtraFiles = []*os.File{reportW}
  helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
  waited := false
  t.Cleanup(func() {
    cancel()
    if !waited && helper.Process != nil { _ = helper.Process.Kill(); _ = helper.Wait() }
    _ = reportR.Close()
    _ = reportW.Close()
    _ = slave.Close()
    terminal.Close()
  })
  if err := helper.Start(); err != nil { t.Fatal(err) }
  reportW.Close()
  slave.Close()
  if _, err := terminal.Master().Write([]byte("x\n")); err != nil { t.Fatal(err) }
  var report foregroundTTYReport
  if err := json.NewDecoder(reportR).Decode(&report); err != nil { t.Fatal(err) }
  waitErr := helper.Wait()
  waited = true
  cancel()
  if waitErr != nil { t.Fatal(waitErr) }
  reportR.Close()
  terminal.Close()
  if report.Err != "" || report.ParentTTYFD <= 3 || report.ParentTTYFD == report.ChildTTYFD || report.ChildTTYFD != 3 || !report.SameTTY || report.Input != "x\n" || !report.RestoredPreviousGroup || !report.RestoredThreadMask || !report.PreservedSIGTTOUNotification || report.DescriptorDelta != 0 {
    t.Fatalf("report=%+v", report)
  }
  assertDescriptorCountReturns(t, baseline)
}

func TestForegroundTTYSessionHelper(t *testing.T) {
  if os.Getenv("GO_WANT_FOREGROUND_TTY_SESSION") != "1" { return }
  reportFile := os.NewFile(3, "foreground-tty-report")
  if reportFile == nil { t.Fatal("missing foreground tty report pipe") }
  t.Cleanup(func() { _ = reportFile.Close() })
  report := runForegroundTTYSession(reportFile)
  if err := json.NewEncoder(reportFile).Encode(report); err != nil { t.Fatal(err) }
  if err := reportFile.Close(); err != nil { t.Fatal(err) }
}

func TestWaitIsSingleUseAndWaitDelayBoundsInheritedPipe(t *testing.T) {
  child := startHelperHoldingStdoutInGrandchild(t, Spec{WaitDelay: 100 * time.Millisecond})
  if err := child.Wait(); !errors.Is(err, ErrWaitDelay) { t.Fatalf("first wait=%v", err) }
  if err := child.Wait(); !errors.Is(err, ErrAlreadyWaited) { t.Fatalf("second wait=%v", err) }
  child.AssertPumpsAndTreeGone(t)
}

func TestWaitDelayClosesBlockingPumpedStreams(t *testing.T) {
  for _, tc := range blockingPumpCases(t) {
    t.Run(tc.name, func(t *testing.T) {
      baseline := snapshotProcessResources(t)
      stream := tc.newBlockingReadWriteCloser()
      child := startHelperWithBlockingPump(t, stream, tc.spec(stream), 100*time.Millisecond)
      <-stream.Blocked()
      err := child.Wait()
      if !errors.Is(err, tc.want) || stream.CloseCalls() != 1 { t.Fatalf("wait=%v closes=%d", err, stream.CloseCalls()) }
      assertChannelClosed(t, child.PumpsDone())
      assertProcessResourcesReturn(t, baseline)
    })
  }
}

func TestWaitDelayClosesSharedPumpedCloserOnce(t *testing.T) {
  baseline := snapshotProcessResources(t)
  stream := newBlockingReadWriteCloser()
  child := startHelperWithBlockingPump(t, stream, Spec{Stdin: stream, Stdout: stream, Stderr: stream}, 100*time.Millisecond)
  stream.WaitUntilReadAndTwoWritesBlocked(t)
  err := child.Wait()
  if !errors.Is(err, ErrWaitDelay) || stream.CloseCalls() != 1 { t.Fatalf("wait=%v closes=%d", err, stream.CloseCalls()) }
  assertChannelClosed(t, child.PumpsDone())
  assertProcessResourcesReturn(t, baseline)
}

type valueCloserWithDynamicData struct { Dynamic any }
func (valueCloserWithDynamicData) Read([]byte) (int, error) { return 0, io.EOF }
func (valueCloserWithDynamicData) Close() error { return nil }

func TestRejectsNonIdentifiableValueCloserBeforeAttempt(t *testing.T) {
  marker := filepath.Join(t.TempDir(), "started")
  helper := helperCommand(t, "mark-start", marker)
  stream := valueCloserWithDynamicData{Dynamic: []byte("non-comparable")}
  var events []ProcessEvent
  err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }).Run(context.Background(), Spec{Path: helper.Path, Args: helper.Args, Stdin: stream, Containment: ContainmentOwnTree})
  if !errors.Is(err, ErrInvalidStream) || len(events) != 0 { t.Fatalf("events=%+v err=%v", events, err) }
  if _, statErr := os.Stat(marker); !errors.Is(statErr, fs.ErrNotExist) { t.Fatalf("process started: %v", statErr) }
}

func TestOrdinaryCompletionDoesNotClosePumpedCloser(t *testing.T) {
  stream := newFiniteReadWriteCloser([]byte("input"))
  if err := (Runner{}).Run(context.Background(), helperSpecWithCooperativeStream(t, stream)); err != nil { t.Fatal(err) }
  if stream.CloseCalls() != 0 { t.Fatalf("ordinary completion closed caller stream %d times", stream.CloseCalls()) }
}

func TestExitErrorAndContextPrecedence(t *testing.T) {
  assertExitCode(t, runHelper(t, context.Background(), "exit", "23"), 23)
  ctx, cancel := context.WithCancel(context.Background())
  child := startBlockingHelper(t, ctx); cancel()
  if err := child.Wait(); !errors.Is(err, context.Canceled) { t.Fatalf("cancel wait=%v", err) }
}

func TestObserveAttemptWithoutStartWhenExecutableIsMissing(t *testing.T) {
  var events []ProcessEvent
  err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }).Run(context.Background(), Spec{Path: missingExecutable(t), Containment: ContainmentOwnTree})
  if !errors.Is(err, exec.ErrNotFound) || len(events) != 1 || events[0].Phase != "attempt" { t.Fatalf("events=%+v err=%v", events, err) }
}

func TestKqueueEventValidation(t *testing.T) {
  const pid = 42
  registrationOK := exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true, Data: 0}}}
  waitOK := exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Exit: true}}}
  cases := []struct { name string; registration, wait exitObserverResult; wantErr bool }{
    {"valid", registrationOK, waitOK, false},
    {"registration-syscall-error", exitObserverResult{Err: syscall.EBADF}, waitOK, true},
    {"registration-zero-count", exitObserverResult{}, waitOK, true},
    {"registration-errno", exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true, Data: int64(syscall.EINVAL)}}}, waitOK, true},
    {"wait-syscall-error", registrationOK, exitObserverResult{Err: syscall.EINTR}, true},
    {"wait-zero-count", registrationOK, exitObserverResult{}, true},
    {"wait-out-of-range-count", registrationOK, exitObserverResult{N: 2, Events: []exitObserverEvent{{PID: pid, Process: true, Exit: true}}}, true},
    {"wait-error-event", registrationOK, exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true, Error: true, Data: int64(syscall.ECHILD)}}}, true},
    {"wait-mismatched-pid", registrationOK, exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid + 1, Process: true, Exit: true}}}, true},
    {"wait-missing-note-exit", registrationOK, exitObserverResult{N: 1, Events: []exitObserverEvent{{PID: pid, Process: true}}}, true},
  }
  for _, tc := range cases {
    t.Run(tc.name, func(t *testing.T) {
      err := validateKqueueObserverResults(pid, tc.registration, tc.wait)
      if (err != nil) != tc.wantErr { t.Fatalf("err=%v wantErr=%v", err, tc.wantErr) }
    })
  }
}

func TestSanitizeEnvRejectsMaliciousInheritedDefaults(t *testing.T) {
  got := SanitizeEnv([]string{"PATH=/bin", "FZF_DEFAULT_OPTS=--listen 9999", "FZF_DEFAULT_OPTS_FILE=/tmp/x", "FZF_DEFAULT_COMMAND=sh -c id", "SHELL_PICKER_TOKEN=stale"}, map[string]string{"SHELL_PICKER_TOKEN": "fresh"})
  assertEnv(t, got, []string{"PATH=/bin", "SHELL_PICKER_TOKEN=fresh"})
}
```

- [ ] **Step 2: Run focused tests to prove the runner is absent**

Run: `go test ./internal/process -run 'Test(RunnerPasses|CancelKills|ForegroundTree|WaitIsSingleUse|WaitDelayCloses|RejectsNonIdentifiable|OrdinaryCompletion|ExitError|Kqueue|SanitizeEnv)' -count=1`

Expected: FAIL with `undefined: Runner`.

- [ ] **Step 3: Define single-wait, bounded-pump, and error contracts**

```go
type Containment uint8
const (
  ContainmentOwnTree Containment = iota + 1
  ContainmentForegroundTree
  ContainmentInheritTree
)

type Spec struct {
  Path string
  Args []string
  Env []string
  Dir string
  Stdin io.Reader
  Stdout io.Writer
  Stderr io.Writer
  Containment Containment
  ForegroundTTY *os.File
  WaitDelay time.Duration
}

type ProcessEvent struct { Phase string; PID int; Path string }
type Runner struct { Observe func(ProcessEvent) }
type ExitError struct { Code uint32 }
func (e *ExitError) Error() string
func (e *ExitError) ExitCode() int
var ErrAlreadyWaited = errors.New("process: Wait called more than once")
var ErrWaitDelay = errors.New("process: I/O pumps did not finish before WaitDelay")
var ErrInvalidStream = errors.New("process: pumped io.Closer requires stable pointer identity")
var ErrExitObserver = errors.New("process: exit observer failed")
var ErrUnsupportedPlatform = errors.New("process: unsupported Unix process backend")
```

Reject negative `WaitDelay`; zero means no post-exit pump deadline. Keep the exact public stream fields `Stdin io.Reader`, `Stdout io.Writer`, and `Stderr io.Writer`. A direct `*os.File` stays caller-owned, is attached directly when the platform launcher can do so, and is never closed by `Runner`. A non-file stream uses a pump. Supported pumped streams must be finite/nonblocking/cooperative, or implement `io.Closer` with a nonblocking `Close` that promptly unblocks every pending `Read` or `Write`. Before emitting `attempt`, executable lookup, stream preparation that can launch work, or process launch, validate every non-file dynamic stream value that implements `io.Closer`: it must expose stable reference identity, normally as a non-nil pointer. Reject non-pointer/value closers, typed-nil pointer closers, and any other non-identifiable closer with `ErrInvalidStream`; do not compare interface values, fields, or complete values structurally and do not use `reflect.DeepEqual` for closer registration. Register emergency closers strictly by pointer identity, so one pointer shared across stdin/stdout/stderr has one registration and at most one `Close`; distinct pointers remain distinct even when their contents match. Supplying a valid pumped closer explicitly grants `Runner` permission to call `Close` only after cancellation or `WaitDelay` emergency cleanup; ordinary successful or failed process completion does not close it. On emergency cleanup, first close runner-owned pipe ends, then close each registered caller-supplied pumped closer at most once, join every pump, and return resources to baseline. Do not claim universal bounded cleanup for arbitrary nonclosable user implementations that block forever: those violate the API precondition and Go cannot forcibly stop the blocked method or its goroutine.

Process observation phases are exactly `attempt|start|exit`: emit `attempt` immediately before executable lookup/launch, emit `start` only after the OS process has been created, and emit `exit` only after reaping and pump cleanup. Missing executables and spawn failures therefore emit one attempt and zero starts/exits. `Wait` is exactly-once and owns reaper completion, pump joining, retained-handle closure, permitted emergency stream closure, and the final `exit` observation. Return errors in this exact precedence: cancellation cause when cancellation won before observed process exit; otherwise nonzero `*ExitError`; otherwise the first pump read/write error; otherwise an `ErrExitObserver`-wrapped registration/wait error; otherwise `ErrWaitDelay`; otherwise nil. Blocking read-closer and write-closer tests cover `ErrWaitDelay`, nonzero-exit, pump-error, and cancellation precedence, prove emergency `Close` is called once and unblocks the pump, and prove goroutines/descriptors/handles/processes return to baseline. Shared-pointer coverage assigns one blocking closer to stdin/stdout/stderr and proves exactly one emergency `Close`. A hostile value-closer fixture contains an `any` field whose dynamic value is a slice, proving rejection before `attempt` or process start without panic from interface or structural comparison. Keep descendant-retained-pipe tests as a separate case because closing runner-owned pipe ends and terminating descendants remains required. A second `Wait` returns `ErrAlreadyWaited` without touching resources. `Run` is `Start` followed by the one `Wait`.

- [ ] **Step 4: Implement Unix owned, foreground, and inherited group semantics**

Put `//go:build !windows` on `process_unix.go` and `process_unix_test.go`, `//go:build linux` on `process_linux.go`, `//go:build freebsd` on `process_kqueue_freebsd.go`, and `//go:build !windows && !linux && !freebsd` on `process_unsupported_unix.go`. Linux and FreeBSD are the implemented Unix process backends; every other Unix target returns `ErrUnsupportedPlatform` during `Start` preflight before stream preparation, `attempt`, executable lookup, or process launch. Unix `ContainmentOwnTree` uses `exec.Cmd` with `SysProcAttr.Setpgid = true`, Linux `Pdeathsig = SIGKILL`, and group kill on cancellation. `ContainmentForegroundTree` requires a non-nil terminal `*os.File`, records its current foreground process group, records `childTTYFD := 3 + len(cmd.ExtraFiles)` (normally 3), and appends caller-owned `ForegroundTTY` to `exec.Cmd.ExtraFiles` so exec duplicates/maps it to that child descriptor for child-side use. On Linux set `SysProcAttr{Setpgid:true, Foreground:true, Ctty:int(ForegroundTTY.Fd()), Pdeathsig:SIGKILL}`; on FreeBSD set the same common fields without Linux-only `Pdeathsig`. Go's `Foreground` path performs `TIOCSPGRP` before fd remapping, so `Ctty` must be the parent descriptor and must not be `childTTYFD`; this transfers the new process group to the foreground atomically during launch. This differs from `Setctty`, whose `Ctty` is a descriptor in the child. fzf stdin/stdout may remain independently remapped pipes. The caller-owned parent tty stays open through process wait and foreground restoration, the child descriptor dies with the child, all start/error paths release runner-owned descriptors, and the runner never closes the caller's `*os.File`.

FreeBSD uses kqueue `EVFILT_PROC|NOTE_EXIT` only as a non-reaping exit observer; `Wait` remains the sole reaper. Register with an event receipt and validate both the syscall error and returned event count. A registration receipt succeeds only when the returned event is the expected process event and any `EV_ERROR` receipt has `Data == 0`; nonzero `Data` is an errno and fails registration. For the wait, inspect exactly the first `n` returned events, require `n > 0`, reject any `EV_ERROR` event using its `Data` errno, and require a matching PID/filter with `NOTE_EXIT` before reporting observed exit. A syscall error, zero events, an out-of-range count, malformed/mismatched event, or nonzero `EV_ERROR` errno is observer failure wrapped with `ErrExitObserver`, never a successful exit; terminate when required, reap the child, and close kqueue/descriptors on every path. Keep event validation behind an injectable, platform-neutral result validator so Linux unit tests cover registration error, wait error, `n == 0`, out-of-range `n`, `EV_ERROR` with nonzero `Data` errno, mismatched PID/filter, missing `NOTE_EXIT`, successful zero-errno registration receipt, and successful exit. Inject the backend selector in a separate preflight test to prove an unsupported Unix backend returns `ErrUnsupportedPlatform` with zero observation events and no start marker. If a FreeBSD runtime is unavailable, those injected tests plus FreeBSD target cross-compilation are mandatory rather than silently treating an unexecuted kqueue path as covered.

Restore foreground ownership without changing process-global signal disposition: call `runtime.LockOSThread`, use `golang.org/x/sys/unix.PthreadSigmask` (or its supported equivalent) to atomically save the exact current thread mask while blocking `SIGTTOU`, perform bounded EINTR-aware `TIOCSPGRP`, restore that exact saved mask with `SIG_SETMASK` on every success/error path, and only then call `runtime.UnlockOSThread`. Never call `os/signal.Ignore`, `signal.Ignore`, `signal.Reset`, or otherwise alter the caller's SIGTTOU handler/notification state. Preserve the primary process result when an already-closed/noncontrolling terminal or cleanup error occurs. The foreground helper tests install caller-owned `signal.Notify` state for `SIGTTOU`, verify a notification still arrives after restoration, compare the thread mask before and after successful restoration, and inject ioctl/mask failures where the platform APIs permit to prove exact mask restoration and thread unlock on error paths. Unix `ContainmentInheritTree` leaves `Setpgid` false. Its `KillTree` sends termination to the inherited callback process group rather than only the renderer PID; for a production preview callback this intentionally kills the callback itself and every renderer descendant. A native/fake renderer that has no external child may return `ErrOutputLimit` or `context.DeadlineExceeded` normally. Tests launch inherited-group behavior from a helper subprocess so killing that group cannot kill the `go test` process.

The foreground unit test also uses an outer same-binary helper rather than assigning a controlling terminal to the `go test` process. The outer test opens the PTY and a separate report pipe, starts the helper on the slave with stdin/stdout/stderr attached there and `SysProcAttr{Setsid:true, Setctty:true, Ctty:0}`, then closes its slave/report-writer copies; only that helper becomes a session leader with the PTY as controlling terminal. This outer `Setctty` setup remains unchanged because `Setctty` correctly interprets `Ctty:0` as a child fd. Inside the helper, `runForegroundTTYSession` records a descriptor baseline and the helper's foreground process group, keeps report fd 3 occupied and reserves descriptors if needed, then opens `/dev/tty` as a caller-owned fd greater than and different from child fd 3. It creates independently remapped stdin/stdout/stderr pipes and invokes `Runner` with `ContainmentForegroundTree` and that parent fd as `ForegroundTTY`; the runner sets `Ctty` to the distinct parent fd while also mapping the tty through `ExtraFiles` to child fd 3. The PTY remains in canonical mode, so the outer test writes the complete line `x\n`; foreground launch must succeed, and the runner child compares fd 3 and `/dev/tty` identity, confirms fd 3 refers to the same controlling terminal despite all standard streams being remapped, and reads and asserts the complete canonical `x\n` from fd 3. The existing five-second context bounds the complete exchange. After `Wait`, the helper verifies its prior foreground process group, exact pre-restoration thread signal mask, and caller-installed SIGTTOU notification state were restored; closes the tty, reserved descriptors, and all remapped stream handles; checks its descriptor count returned to baseline; and sends only `foregroundTTYReport` over the separately inherited report pipe. The outer process never calls `Setsid`, `Setctty`, or a foreground-pgrp ioctl; it decodes the report, waits/reaps the helper, closes the report reader and PTY master, and checks its own descriptor baseline. Start, decode, assertion, injected mask/ioctl failure, and failure paths register cleanup for every PTY, pipe, process handle, thread lock, and saved signal mask so no early failure leaks, leaves the helper running, or alters process-global SIGTTOU disposition.

- [ ] **Step 5: Write Windows stream, handle-list, and failure-stage tests before the launcher**

```go
//go:build windows

func TestCreateProcessInheritsOnlyExplicitChildHandles(t *testing.T) {
  result := runHandleProbe(t, Spec{Stdin: nil, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
  if result.UnexpectedInheritedHandles != 0 || result.PumpCount != 2 { t.Fatalf("result=%+v", result) }
}

func TestSharedStdoutStderrWriterIsSerialized(t *testing.T) {
  writer := &concurrencyDetectingWriter{}
  if err := (Runner{}).Run(context.Background(), helperSpec(t, "both-streams", writer, writer)); err != nil { t.Fatal(err) }
  if writer.ConcurrentCalls() != 0 { t.Fatal("stdout and stderr pumps wrote concurrently") }
}

func TestWindowsStartFailureStagesCloseEverything(t *testing.T) {
  for _, stage := range []winStage{
    stageDevNull, stageDuplicate, stagePipe, stagePipeInheritance,
    stageCreateJob, stageConfigureJob, stageAttributeList, stageUpdateHandleList,
    stageCreateProcess, stageAssignJob, stageResumeThread,
  } {
    t.Run(stage.String(), func(t *testing.T) {
      baseline := processHandleCount(t)
      err := startWithInjectedFailure(t, stage)
      if err == nil { t.Fatal("injected stage succeeded") }
      assertHandleCountReturns(t, baseline)
      assertInjectedChildGone(t)
    })
  }
}
```

- [ ] **Step 6: Implement suspended Windows launch, explicit handles, Jobs, pumps, and cleanup**

Put `//go:build windows` on `process_windows.go`, `handles_windows.go`, `job_windows.go`, and all Windows tests. Windows production must not call `exec.Cmd.Start` or `exec.Command`: compose the exact UTF-16 command line and sorted environment, create the Job, and call `x/sys/windows.CreateProcess` with `EXTENDED_STARTUPINFO_PRESENT|CREATE_SUSPENDED|CREATE_UNICODE_ENVIRONMENT`. Build `PROC_THREAD_ATTRIBUTE_HANDLE_LIST` with `windows.NewProcThreadAttributeList`, `Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, ...)`, `List`, and `Delete`; set `bInheritHandles=true`, but include only dedicated inheritable child-side duplicates in the explicit list.

For each nil stream, open `NUL` and duplicate a child-side inheritable handle. For each `*os.File`, duplicate its OS handle directly for the child without creating a pump; the duplicate is runner-owned, but the original remains caller-owned and is never closed by `Runner`. For every supported non-file `io.Reader`/`io.Writer`, create an anonymous pipe, make only its child side inheritable, retain the parent side, and start one pump after successful resume; this yields 0..3 pumps. Enforce the same finite/nonblocking/cooperative-or-promptly-unblocking-`io.Closer` precondition and pre-attempt pointer-identity validation as the common contract. When stdout and stderr refer to the same writer, wrap both pumps in one mutex-serialized writer; when the same closer pointer is shared across any of stdin/stdout/stderr, use the one common identity registration and call it at most once during emergency cleanup. Parent originals, Job/process/thread handles, attribute-list storage, parent pipe sides, and all child duplicates have explicit ownership and are closed exactly once.

The success order is: prepare streams and attribute list; create suspended process; assign it to the non-inheritable kill-on-close Job for `ContainmentOwnTree`/`ContainmentForegroundTree` or nested renderer Job for `ContainmentInheritTree`; resume; close the thread handle and every parent-held child duplicate; start pumps; start one reaper goroutine and one cancellation watcher. Windows ignores `ForegroundTTY`; ConPTY/console attachment is established by the caller's startup attributes and foreground containment otherwise has owned-Job semantics. The reaper calls `WaitForSingleObject`/`GetExitCodeProcess` even after cancellation and signals process exit. The cancellation watcher terminates the Job/tree once and never reaps. After process exit, `Wait` lets pumps drain; if `WaitDelay` expires, close parent pipe sides, terminate any Job descendants still retaining them, call `Close` once on eligible blocking pumped streams, join all pumps, and apply the stated error precedence. Cancellation uses the same permitted emergency close path. Ordinary completion never closes caller streams.

Every failure before process creation closes all prepared duplicates, pipes, devnull handles, attribute storage, and Job handles. Every failure after process creation calls `TerminateProcess`, performs a bounded `WaitForSingleObject`, then closes thread, process, Job, pipe, duplicate, and attribute resources. Inject function variables at each named Win32 stage (`CreateFile(NUL)`, `DuplicateHandle`, `CreatePipe`/inheritance clearing, Job create/configure, attribute-list initialize/update, `CreateProcess`, assign, and resume); production defaults call the real APIs. Separate cleanup-failure tests preserve the primary stage error while still attempting every remaining terminate/wait/close action. Stage tests assert the exact child/handle/pump baseline after each failure, including assign and resume failures.

`SanitizeEnv` always removes `FZF_DEFAULT_OPTS`, `FZF_DEFAULT_OPTS_FILE`, `FZF_DEFAULT_COMMAND`, all keys prefixed `SHELL_PICKER_`, and `FZF_KEY|FZF_QUERY|FZF_CURRENT_ITEM`, then adds the controlled map. Unix keys are case-sensitive. Windows keys use `strings.EqualFold`, retain only the last ordinary inherited duplicate, and emit one canonical entry per controlled key. On both launchers call `Observe` with `attempt` immediately before lookup/launch, `start` immediately after successful OS process creation, and `exit` after reaping and pump cleanup, using only the direct executable path and PID where one exists; missing/spawn failure has one attempt and zero starts, and a nil observer has zero behavior cost.

- [ ] **Step 7: Verify race safety and both OS builds**

Run: `go test -race ./internal/process -count=1 && GOOS=windows GOARCH=amd64 go test -c ./internal/process -o /tmp/shell-picker-process.test.exe && GOOS=freebsd GOARCH=amd64 go test -c ./internal/process -o /tmp/shell-picker-process-freebsd.test`

Expected: PASS with no race report, exact attempt/start/exit observations and exit/cancellation/`WaitDelay` precedence, 0..3 pumps bounded under the supported cooperative stream contract, shared closer pointer deduplication with one emergency `Close`, rejection of a non-identifiable value closer before attempt/start without panic, ordinary completion preserving caller ownership, descendant-retained-pipe coverage, goroutine/handle/descriptor/process return to baseline, malicious defaults removed, injected kqueue event-count/`EV_ERROR`/`Data` validation passing, unsupported Unix rejection before launch, Go-supported parent-fd `Foreground` Ctty semantics and atomic transfer plus child fd 3 TTY mapping/restoration proven with complete canonical `x\n` input, exact thread-mask and caller-installed SIGTTOU notification preservation without process-global disposition changes, fully remapped stdio, distinct descriptors, owned/inherited Unix group assertions passing, and successful Windows low-level launcher/handle-list/Job and FreeBSD kqueue target compilation. No bounded-cleanup result is claimed for a nonclosable user stream whose method blocks forever in violation of the API precondition.

- [ ] **Step 8: Commit process control**

```bash
git add internal/process
git commit -m "feat(process): add cancellable direct process trees"
```

### Task 6: Implement Batched Local Candidate Enumeration and Ordering

**Files:**
- Create: `internal/candidate/record.go`
- Create: `internal/candidate/local.go`
- Create: `internal/candidate/local_unix.go`
- Create: `internal/candidate/local_windows.go`
- Create: `internal/candidate/local_unix_test.go`
- Create: `internal/candidate/local_windows_test.go`

**Interfaces:**
- Consumes: `protocol.Picker`, the current `pathutil.Location` (an absolute filesystem path or Drives), and an optional symlink worker count.
- Produces: `candidate.Record` with an authoritative `pathutil.Location` target, `candidate.Record.Wire() protocol.WireRecord`, `candidate.Record.FullKey() string`, `candidate.LocalOptions`, and `candidate.EnumerateLocal(context.Context, protocol.Picker, pathutil.Location, candidate.LocalOptions) ([]candidate.Record, error)`.

- [ ] **Step 1: Write ordering, hidden, ignored, unusual-byte, symlink, and Drives tests**

```go
func TestEnumerateCPOrderAndIdentity(t *testing.T) {
  root := makeCandidateTree(t, []entry{
    {".hidden-dir", dir}, {"VisibleDir", dir}, {".hidden-file", file}, {"visible file", file},
    {"ignored", dir}, {"link-dir", symlinkDir}, {"bad-\xff", file},
  })
  records, err := EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte(root)), LocalOptions{StatWorkers: 4})
  if err != nil { t.Fatal(err) }
  assertDisplays(t, records, []string{".", "..", ".hidden-dir/", "ignored/", "link-dir/", "VisibleDir/", ".hidden-file", `bad-\xFF`, "visible file"})
  for _, record := range records {
    decoded, err := protocol.DecodePath(record.Wire().Payload)
    if err != nil || !bytes.Equal(decoded, record.Path) { t.Fatalf("record=%+v err=%v", record, err) }
  }
}

func TestEnumerateCDIncludesIgnoredDirectoriesAndNoFiles(t *testing.T) {
  root := makeIgnoredTree(t)
  records, err := EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Filesystem([]byte(root)), LocalOptions{StatWorkers: 2})
  if err != nil { t.Fatal(err) }
  assertDisplays(t, records, []string{".", "..", ".git", ".worktrees", "visible"})
}

func TestDeterministicFoldedOrderUsesRawByteTie(t *testing.T) {
  names := [][]byte{[]byte("a"), []byte("A"), []byte("ä"), []byte("Ä"), {'a', 0xff}, {'A', 0xff}}
  sort.Slice(names, func(i, j int) bool { return lessFolded(names[i], names[j]) })
  assertByteNames(t, names, [][]byte{[]byte("A"), []byte("a"), {'A', 0xff}, {'a', 0xff}, []byte("Ä"), []byte("ä")})
}
```

In `local_windows_test.go`, add a lexical root-prefix test that performs no network access and covers both a `C:\` drive-root location and a `\\server\share\` UNC-share-root location:

```go
func TestWindowsDriveAndUNCRootsUseCanonicalVirtualDrivesParent(t *testing.T) {
  roots := []pathutil.Location{
    pathutil.Filesystem([]byte(`C:\`)),
    pathutil.Filesystem([]byte(`\\server\share\`)),
  }
  for _, root := range roots {
    for _, picker := range []protocol.Picker{protocol.PickerCD, protocol.PickerCP} {
      records := rootRecords(picker, root)
      assertDisplays(t, records, []string{".", ".."})
      for i, record := range records {
        parsed, err := protocol.ParseRecord(record.Wire().Bytes())
        if err != nil { t.Fatalf("picker=%s root=%q record=%+v err=%v", picker, root.Path, record, err) }
        decoded, err := protocol.DecodePath(parsed.Payload)
        if err != nil || len(decoded) == 0 { t.Fatalf("picker=%s root=%q payload=%q err=%v", picker, root.Path, parsed.Payload, err) }
        if i == 1 {
          if parsed.Kind != protocol.KindVirtual || parsed.Display != ".." || !bytes.Equal(decoded, []byte("drives")) || record.Target.Kind != pathutil.KindDrives || len(record.Path) != 0 {
            t.Fatalf("virtual picker=%s root=%q record=%+v decoded=%q", picker, root.Path, record, decoded)
          }
        } else if record.Target.Kind != pathutil.KindFilesystem || !bytes.Equal(decoded, record.Path) {
          t.Fatalf("ordinary picker=%s root=%q record=%+v decoded=%q", picker, root.Path, record, decoded)
        }
      }
    }
  }
}
```

Also test a non-root filesystem location to prove `..` remains an ordinary filesystem record whose payload decodes to its absolute parent and whose `Target` is `pathutil.Filesystem(record.Path)`, and test `pathutil.Drives()` to prove it still returns only ordered `KindDrive` records with no `.` or `..`.

- [ ] **Step 2: Run candidate tests to verify enumeration is missing**

Run: `go test ./internal/candidate -run 'TestEnumerateCPOrderAndIdentity|TestEnumerateCDIncludesIgnoredDirectoriesAndNoFiles' -count=1`

Expected: FAIL with `undefined: EnumerateLocal`.

- [ ] **Step 3: Implement one batch reader and one bounded symlink pool**

```go
type Record struct {
  Kind protocol.Kind
  Display string
  Path []byte
  Payload string
  Target pathutil.Location
}

func (r Record) Wire() protocol.WireRecord {
  return protocol.WireRecord{Kind: r.Kind, Display: r.Display, Payload: r.Payload}
}

type LocalOptions struct { StatWorkers int }
```

Put `//go:build !windows` on `local_unix.go` and `local_unix_test.go`, and `//go:build windows` on their Windows counterparts. Open the directory once and call `ReadDir(128)` until `io.EOF`. Classify non-symlinks inline; send only symlink indices through a fixed worker pool sized `max(2, min(8, requested-or-GOMAXPROCS))`; workers call `os.Stat` and return by index. `lessFolded` decodes valid UTF-8 runes and applies `unicode.ToLower`, preserves each invalid byte unchanged, compares folded byte strings, then compares original raw bytes as the tie breaker; checked fixtures make ordering independent of locale. Emit `.` and `..` first, then hidden directories, visible directories, hidden files, and visible files; `cd` omits files and uses kind `local`, while `cp` uses `directory` and `file` and appends `/` only to directory display. Reject names containing NUL; slash cannot occur inside a directory entry. Do not consult ignore files.

Every ordinary local/drive record sets `Path` to its absolute filesystem bytes, `Target` to `pathutil.Filesystem(Path)`, and `Payload` to `protocol.EncodePath(Path)`. Keep `rootRecords` as a small platform helper used before child enumeration. On Unix and at non-root Windows filesystem locations, it returns ordinary filesystem `.` and `..`. At Windows drive roots and UNC share roots, it returns ordinary filesystem `.` followed by one record with `Kind: protocol.KindVirtual`, `Display: ".."`, no filesystem `Path`, `Payload: protocol.EncodePath([]byte(protocol.VirtualDrivesTarget))`, and `Target: pathutil.Drives()`; child records follow that prefix. Never stat, output, relativize, or otherwise treat the virtual payload token as a filesystem path. On Drives, emit only kind `drive`, ascending `A:\` through `Z:\`, with filesystem targets and no dot records.

- [ ] **Step 4: Verify tests, goroutine bounds, and Windows compilation**

Run: `go test ./internal/candidate -run 'TestEnumerate|TestStatWorkerBound' -count=1 && GOOS=windows GOARCH=amd64 go test -c ./internal/candidate -o /tmp/shell-picker-candidate.test.exe`

Expected: PASS; `TestStatWorkerBound` observes no more than eight concurrent `Stat` calls; drive-root and UNC-share-root records all parse with nonempty canonical payloads, the virtual payload decodes exactly to `drives`, its authoritative target is `KindDrives`, ordinary records retain filesystem targets, non-root `..` stays ordinary, Drives has no dot records; and Windows compilation succeeds.

- [ ] **Step 5: Commit local enumeration**

```bash
git add internal/candidate
git commit -m "feat(candidate): enumerate local paths without shell tools"
```

### Task 7: Add Cached and Fresh Zoxide Generation Policies

**Files:**
- Create: `internal/candidate/zoxide.go`
- Create: `internal/candidate/builder.go`
- Create: `internal/candidate/zoxide_test.go`
- Create: `internal/candidate/builder_test.go`

**Interfaces:**
- Consumes: `candidate.EnumerateLocal`, `process.Runner`, current picker/location, zoxide executable name, sanitized environment, cached/fresh policy, one nonnegative zoxide budget, and caller/session cancellation.
- Produces: `candidate.ZoxidePolicy`, `candidate.ZoxideCached`, `candidate.ZoxideFresh`, `candidate.ParseZoxidePolicy(string) (ZoxidePolicy, error)`, `candidate.ZoxideCache`, `candidate.NewZoxideCache(process.Runner, string, []string, time.Duration) (*candidate.ZoxideCache, error)`, `candidate.DefaultZoxideTimeout() time.Duration`, `(*candidate.ZoxideCache).Load(context.Context) error`, `(*candidate.ZoxideCache).Records() ([]candidate.Record, candidate.SourceMetrics, error)`, `candidate.BuildRequest`, `candidate.SourceMetrics` including zoxide attempts/starts/max-live, `candidate.BuildResult`, and `(*candidate.Builder).Build(context.Context, candidate.BuildRequest) (candidate.BuildResult, error)`; one non-copyable-after-use Builder pointer owns each picker session's fresh-query permit.

- [ ] **Step 1: Write cached initial-overlap, policy attempt/start/max-live, timeout-discard, and fresh-unlimited tests**

```go
func TestInitialBuilderOverlapsCacheLoadAndMergesLocalFirst(t *testing.T) {
  localGate, zoxideGate := make(chan struct{}), make(chan struct{})
  cache := testZoxideCache(t,
    func(context.Context) ([]Record, error) { close(zoxideGate); <-localGate; return zoxideRecords("visible", "/z/one", "/z/two"), nil },
    75*time.Millisecond,
  )
  b := testBuilder(cache,
    func(context.Context, BuildRequest) ([]Record, error) { close(localGate); <-zoxideGate; return localRecords(".", "..", ".hidden", "visible"), nil },
  )
  b.Policy = ZoxideCached
  got, err := b.Build(context.Background(), BuildRequest{Picker: protocol.PickerCD, Location: testLocation(), Initial: true})
  if err != nil { t.Fatal(err) }
  assertPaths(t, got.Records, []string{".", "..", ".hidden", "visible", "/z/one", "/z/two"})
}

func TestCachedPolicyAttemptsOnceAndSuccessfulToolStartsOnceForSession(t *testing.T) {
  runner := countingZoxide(t, []byte("/z/one\n"), nil)
  cache := mustNewZoxideCache(t, runner, "zoxide", sanitizedTestEnv(), 75*time.Millisecond)
  b := &Builder{Cache: cache, Policy: ZoxideCached, enumerate: fakeLocal}
  mustBuild(t, b, BuildRequest{Picker: protocol.PickerCD, Location: location("/one"), Initial: true})
  mustBuild(t, b, BuildRequest{Picker: protocol.PickerCD, Location: location("/two")})
  mustBuild(t, b, BuildRequest{Picker: protocol.PickerCD, Location: location("/three")})
  if runner.Attempts() != 1 || runner.Starts() != 1 || runner.MaxLive() != 1 { t.Fatalf("zoxide counts=%+v", runner.Counts()) }
}

func TestFreshPolicyAttemptsEveryCDGenerationAndSuccessfulToolStartsPerGeneration(t *testing.T) {
  runner, overlap := gatedZoxidePerStart(t, []byte("/z/one\n"))
  b := freshBuilder(t, runner, 75*time.Millisecond)
  for _, request := range []BuildRequest{
    {Picker: protocol.PickerCD, Location: location("/one"), Initial: true},
    {Picker: protocol.PickerCD, Location: location("/two")},
  } {
    pending := asyncBuild(b, request)
    overlap.AssertLocalAndZoxideStarted(t)
    overlap.Release(t)
    pending.Result(t)
  }
  if runner.Attempts() != 2 || runner.Starts() != 2 || runner.MaxLive() != 1 { t.Fatalf("zoxide counts=%+v", runner.Counts()) }
}

func TestCPNeverCreatesOrLoadsZoxideCacheUnderEitherPolicy(t *testing.T) {
  for _, policy := range []ZoxidePolicy{ZoxideCached, ZoxideFresh} {
    t.Run(fmt.Sprint(policy), func(t *testing.T) {
      runner := countingZoxide(t, []byte("/z/one\n"), nil)
      b := &Builder{Policy: policy, enumerate: fakeLocal}
      if policy == ZoxideCached {
        b.Cache = mustNewZoxideCache(t, runner, "zoxide", sanitizedTestEnv(), 75*time.Millisecond)
      } else {
        b.NewCache = func() (*ZoxideCache, error) { t.Fatal("cp invoked fresh cache factory"); return nil, nil }
      }
      mustBuild(t, b, BuildRequest{Picker: protocol.PickerCP, Location: location("/one"), Initial: true})
      if runner.Attempts() != 0 || runner.Starts() != 0 { t.Fatalf("policy=%v zoxide counts=%+v", policy, runner.Counts()) }
    })
  }
}

func TestZoxideLookupAndSpawnFailureAttemptsWithoutStart(t *testing.T) {
  for _, tc := range []struct{ name string; err error }{
    {"missing", exec.ErrNotFound},
    {"spawn-failure", errors.New("injected spawn failure")},
  } {
    t.Run(tc.name, func(t *testing.T) {
      runner := failingZoxideRunner(t, tc.err)
      got := mustBuild(t, &Builder{Cache: mustNewZoxideCache(t, runner, "zoxide", sanitizedTestEnv(), 75*time.Millisecond), Policy: ZoxideCached, enumerate: fakeLocal}, initialCDRequest())
      if runner.Attempts() != 1 || runner.Starts() != 0 || runner.MaxLive() != 0 { t.Fatalf("counts=%+v", runner.Counts()) }
      if got.Metrics.ZoxideAttempts != 1 || got.Metrics.ZoxideStarts != 0 || got.Metrics.ZoxideMaxLive != 0 { t.Fatalf("metrics=%+v", got.Metrics) }
    })
  }
}

func TestZoxideTimeoutSoftDiscardsAllPartialOutput(t *testing.T) {
  runner := blockingZoxideAfterOutput(t, []byte("/z/partial\n"))
  cache := mustNewZoxideCache(t, runner, "zoxide", sanitizedTestEnv(), 20*time.Millisecond)
  got := mustBuild(t, &Builder{Cache: cache, Policy: ZoxideCached, enumerate: fakeLocal}, initialCDRequest())
  if containsPath(got.Records, "/z/partial") || !got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "timeout" {
    t.Fatalf("result=%+v", got)
  }
  runner.AssertTreeGone(t)
}

func TestCallerCancellationBeforePrivateTimeoutWinsAndReaps(t *testing.T) {
  cause := errors.New("caller cancelled first")
  runner := blockingZoxideAfterOutput(t, []byte("/z/partial\n"))
  b := &Builder{Cache: mustNewZoxideCache(t, runner, "zoxide", sanitizedTestEnv(), 250*time.Millisecond), Policy: ZoxideCached, enumerate: fakeLocal}
  ctx, cancel := context.WithCancelCause(context.Background())
  pending := asyncBuildContext(b, ctx, initialCDRequest())
  runner.WaitStarted(t)
  cancel(cause)
  got, err := pending.ResultWithError(t)
  if !errors.Is(err, cause) || len(got.Records) != 0 || containsPath(got.Records, "/z/partial") { t.Fatalf("result=%+v err=%v", got, err) }
  if runner.Attempts() != 1 || runner.Starts() != 1 || runner.Exits() != 1 || runner.Live() != 0 { t.Fatalf("counts=%+v", runner.Counts()) }
  runner.AssertTreeGone(t)
}

func TestFreshBuilderSerializesSessionQueriesAndCancelledWaiterDoesNotAttempt(t *testing.T) {
  runner, releases := releasableZoxidePerStart(t, []byte("/z/one\n"))
  b, factoryCalls := freshBuilderWithFactoryCount(t, runner, 0)
  first := asyncBuild(b, initialCDRequest())
  runner.WaitForStarts(t, 1)

  cause := errors.New("cancelled behind permit")
  waiterCtx, cancelWaiter := context.WithCancelCause(context.Background())
  waiter := asyncBuildContext(b, waiterCtx, laterCDRequest())
  cancelWaiter(cause)
  waiterResult, waiterErr := waiter.ResultWithin(t, 100*time.Millisecond)
  if !errors.Is(waiterErr, cause) || len(waiterResult.Records) != 0 { t.Fatalf("result=%+v err=%v", waiterResult, waiterErr) }
  if factoryCalls.Load() != 1 || runner.Attempts() != 1 || runner.Starts() != 1 || runner.MaxLive() != 1 { t.Fatalf("factory=%d counts=%+v", factoryCalls.Load(), runner.Counts()) }

  releases.ReleaseNext(t)
  first.Result(t)
  next := asyncBuild(b, laterCDRequest())
  runner.WaitForStarts(t, 2)
  releases.ReleaseNext(t)
  next.Result(t)
  if factoryCalls.Load() != 2 || runner.Attempts() != 2 || runner.Starts() != 2 || runner.Exits() != 2 || runner.MaxLive() != 1 || runner.Live() != 0 { t.Fatalf("factory=%d counts=%+v", factoryCalls.Load(), runner.Counts()) }
}

func TestIndependentFreshSessionBuildersMayQueryConcurrently(t *testing.T) {
  runner, release := holdAllStartedZoxide(t, 2, []byte("/z/one\n"))
  first := asyncBuild(freshBuilder(t, runner, 0), initialCDRequest())
  second := asyncBuild(freshBuilder(t, runner, 0), initialCDRequest())
  runner.WaitForStarts(t, 2)
  if runner.MaxLive() != 2 { t.Fatalf("counts=%+v", runner.Counts()) }
  release()
  first.Result(t)
  second.Result(t)
}

func TestFreshZeroTimeoutIsAuthoritativeUnlimitedPerGeneration(t *testing.T) {
  runner, releases := releasableZoxidePerStart(t, []byte("/z/one\n"))
  b := freshBuilder(t, runner, 0)
  for _, request := range []BuildRequest{initialCDRequest(), laterCDRequest()} {
    pending := asyncBuild(b, request)
    pending.AssertBlocked(t)
    releases.ReleaseNext(t)
    if got := pending.Result(t); !containsPath(got.Records, "/z/one") { t.Fatalf("result=%+v", got) }
  }
  if runner.Attempts() != 2 || runner.Starts() != 2 || runner.MaxLive() != 1 { t.Fatalf("zoxide counts=%+v", runner.Counts()) }
}
```

- [ ] **Step 2: Run the merge tests to verify Builder is absent**

Run: `go test ./internal/candidate -run 'Test(InitialBuilder|CachedPolicy|FreshPolicy|CPNever|ZoxideTimeout|FreshZero)' -count=1`

Expected: FAIL with `undefined: NewZoxideCache`, `undefined: ZoxideCached`, and `BuildRequest.Initial undefined`.

- [ ] **Step 3: Define the immutable cache, build request, metrics, and platform defaults**

```go
type BuildRequest struct { Picker protocol.Picker; Location pathutil.Location; StatWorkers int; Initial bool }
type SourceMetrics struct {
  LocalDuration time.Duration
  ZoxideDuration time.Duration
  ZoxideOutcome string
  ZoxideAttempts int
  ZoxideStarts int
  ZoxideMaxLive int
}
type BuildResult struct { Records []Record; ZoxideDiscarded bool; Metrics SourceMetrics }
type Builder struct {
  Cache *ZoxideCache
  Policy ZoxidePolicy
  NewCache func() (*ZoxideCache, error)
  enumerate func(context.Context, protocol.Picker, pathutil.Location, LocalOptions) ([]Record, error)
  freshPermitOnce sync.Once
  freshPermit chan struct{}
}
type ZoxidePolicy uint8
const (ZoxideCached ZoxidePolicy = iota + 1; ZoxideFresh)
type ZoxideCache struct {
  once sync.Once
  ready chan struct{}
  runner process.Runner
  path string
  environment []string
  timeout time.Duration
  records []Record
  metrics SourceMetrics
  err error
}
```

`NewZoxideCache` rejects a negative timeout, copies its environment, initializes `ready`, and leaves records private. `DefaultZoxideTimeout` returns exactly `75*time.Millisecond` when `runtime.GOOS != "windows"` and `150*time.Millisecond` on Windows. A zero timeout means no cache-specific deadline. `Load` performs the `sync.Once` invocation attempt and waits for `ready`; `Records` fails before `ready`, then returns cloned records plus copied metrics, so no generation can mutate cached state. `sync.Once` covers the complete attempt, including missing executable, spawn failure, timeout, malformed output, and cancellation; therefore one cache makes at most one `zoxide query --list` attempt and has zero or one successful OS start.

`ParseZoxidePolicy` accepts exactly `cached` and `fresh` and rejects empty, mixed-case, or any other value. `Builder.Policy` must be one of the two constants. `ZoxideCached` requires one app-owned session `Cache` and no `NewCache`; `ZoxideFresh` requires a `NewCache` factory and no shared `Cache`, so each generation receives isolated `sync.Once`, records, metrics, deadline, and process state. Each picker session constructs and fully configures exactly one `*Builder` before first use; because the Builder contains synchronization state, it must not be copied after any `Build` call begins.

- [ ] **Step 4: Implement transactional loading and policy-specific local-first merges**

For `ZoxideCached`, the initial `cd` request starts local enumeration and the app-owned session `cache.Load` before waiting for either. The completed cache is immutable for the rest of the picker session. It makes exactly one invocation attempt for the session, records zero or one successful OS start and at most one live process, and never attempts zoxide again. Every later cached `cd` generation starts only local enumeration, clones records from that ready cache, reports `cached`, reports zero attempts/starts for that generation, and merges local first while retaining cache order and deduplicating exact absolute path bytes.

For `ZoxideFresh`, every `cd` build first acquires the Builder's lazily initialized, cancellation-aware one-token permit. Only the permit holder calls `NewCache`, exactly once, to create a generation-local cache, then starts that cache's `Load` while overlapping its own local enumeration. Hold the permit until `Load` has returned and any started process is killed/reaped, then release it on every path. A caller whose context is cancelled while waiting returns `context.Cause(ctx)` promptly, publishes no partial records, never calls `NewCache`, and records no zoxide attempt/start. The generation-local records are immutable for that build and are discarded with the generation; no later generation reuses them. Each completed successful or soft-failed fresh generation therefore makes exactly one `zoxide query --list` invocation attempt, records zero or one successful OS start, and `ZoxideMaxLive` is at most one across all ordinary or maliciously concurrent calls through that session Builder. Independent Builder instances belong to independent picker sessions and may hold their permits and query concurrently; do not add a package- or process-global mutex. With timeout `0`, every permit holder waits without a cache-specific deadline, exactly matching the authoritative unlimited per-generation zoxide query behavior.

`Load` invokes direct `zoxide query --list` with `ContainmentOwnTree`, a finite `process.Spec.WaitDelay`, and `process.SanitizeEnv(cache.environment, nil)`. Aggregate the runner's `attempt|start|exit` observations into `ZoxideAttempts`, `ZoxideStarts`, and `ZoxideMaxLive`; attempt occurs immediately before lookup/launch, start only after OS creation, and a missing executable or spawn failure therefore records attempt 1/start 0/max-live 0. If timeout is nonzero, derive a private budget deadline from the caller; if that budget expires while the caller remains live, kill/reap any started zoxide tree, discard the complete stdout buffer, publish an empty immutable cache, and report the soft outcome `timeout`. If caller/session cancellation occurs first, its exact `context.Cause(ctx)` wins over the private timeout and process-kill/wait errors, the whole generation and every partial record are discarded, and every started process is reaped before return. A local hard failure cancels and waits for the active session or generation cache load before returning.

Capture all zoxide stdout and process status before parsing. The `zoxide query --list` contract returns absolute filesystem paths. Empty output is valid. Remove at most one trailing LF, split remaining bytes on LF, and reject a leading/interior/trailing empty row, any NUL, or any row that is not a valid platform-absolute filesystem path. Accepted rows preserve arbitrary legal pathname bytes within that platform-absolute form; do not clean, normalize, reinterpret, or require UTF-8 beyond platform legality. A relative row makes the entire zoxide result `malformed`, which is a soft failure that publishes an empty cache and retains exact local output. Missing executable, nonzero exit, any other malformed bytes, and timeout behave the same; any bytes received before those outcomes are discarded. Every accepted row uses kind `KindZoxide`, exact absolute bytes in `Path` and the padded payload, `Target: pathutil.Filesystem(Path)`, and `protocol.EscapeDisplay(Path)` as display. This validation applies only to zoxide filesystem rows: `KindVirtual` and the fixed `drives` token are merge inputs from local enumeration and never reach the absolute-path check. Outcomes are exactly `ok|missing|process-error|malformed|timeout|cancelled|not-run|cached`.

For both policies, merge local first while retaining zoxide order. Deduplicate ordinary local/zoxide records by exact authoritative filesystem target bytes, leaving their existing ordering and path behavior unchanged. Handle a `KindVirtual` record by its non-filesystem `Target` plus full wire identity; never pass its `drives` token to absolute-path checks, `filepath` operations, or zoxide deduplication. Add a merge fixture containing the Windows virtual parent beside ordinary local and zoxide records and prove it survives once without changing ordinary deduplication. `cp` never loads or merges zoxide, never invokes `NewCache`, reports `not-run`, and records zero attempts/starts/max-live. Nil `enumerate` selects `EnumerateLocal`; package tests inject only deterministic package-private functions, not provider abstractions.

- [ ] **Step 5: Add parser, default-budget, cancellation, and cached/fresh navigation benchmarks**

Test policy parsing for exactly `cached|fresh`; success with and without one trailing LF; empty output; platform-absolute rows preserving arbitrary legal bytes; a relative row as whole-buffer malformed soft failure; missing executable; injected spawn failure; nonzero exit after partial output; leading/interior empty rows; NUL; exact duplicate removal; virtual-target merge without path interpretation or absolute checking; sanitized environment; timeout before output; timeout after partial output; caller cancellation racing the private timeout with the first caller cause winning, no partial records, and process reaping; zero-budget completion; concurrent cache load calls; cached initial plus repeated navigation; concurrent calls through the same fresh session Builder proving max-live one; a cancelled waiter returning promptly behind an unlimited generation without factory call or attempt; independent session Builders concurrently reaching max-live two to reject a global mutex; fresh repeated generations; `cp` under both policies; and `75ms` Linux/`150ms` Windows defaults. Assert cached makes exactly one attempt per session, zero or one successful start, matching exits/process counts, at most one live, and no later attempts; fresh makes exactly one attempt per completed `cd` generation, at most one successful start per generation, matching exits/process counts, and at most one live per session Builder; `cp` records zero attempts/starts/exits/processes; missing/spawn-failure cases assert attempt 1/start 0; and every started process is reaped.

```go
func BenchmarkInitialZoxideOverlap(b *testing.B) {
  runner := benchmarkCountingZoxide(b)
  for i := 0; i < b.N; i++ {
    builder := newBenchmarkInitialBuilder(b, runner, immediateZoxide(10_000))
    if _, err := builder.Build(context.Background(), benchmarkInitialRequest()); err != nil { b.Fatal(err) }
  }
  if runner.Attempts() != b.N || runner.Starts() != b.N || runner.Exits() != b.N || runner.Processes() != b.N || runner.MaxLive() != 1 || runner.Live() != 0 { b.Fatalf("zoxide counts=%+v generations=%d", runner.Counts(), b.N) }
}

func BenchmarkZoxideTimeoutDiscard(b *testing.B) {
  for i := 0; i < b.N; i++ {
    builder, runner := newBenchmarkTimeoutBuilder(b, partialThenDeadlineZoxide())
    got, err := builder.Build(context.Background(), benchmarkInitialRequest())
    if err != nil || !got.ZoxideDiscarded || got.Metrics.ZoxideOutcome != "timeout" { b.Fatalf("got=%+v err=%v", got, err) }
    if runner.Attempts() != 1 || runner.Starts() != 1 || runner.Exits() != 1 || runner.Processes() != 1 || runner.MaxLive() != 1 || runner.Live() != 0 { b.Fatalf("zoxide counts=%+v", runner.Counts()) }
  }
}

func BenchmarkCachedZoxideNavigation(b *testing.B) {
  builder, runner := readyCachedBuilder(b, 10_000)
  request := BuildRequest{Picker: protocol.PickerCD, Location: benchmarkLocation()}
  attemptsBefore := runner.Attempts()
  b.ReportAllocs()
  for i := 0; i < b.N; i++ {
    if _, err := builder.Build(context.Background(), request); err != nil { b.Fatal(err) }
  }
  if attemptsBefore != 1 || runner.Attempts() != attemptsBefore || runner.Starts() != 1 || runner.Exits() != 1 || runner.Processes() != 1 || runner.MaxLive() != 1 || runner.Live() != 0 { b.Fatalf("zoxide counts=%+v", runner.Counts()) }
}

func BenchmarkFreshZoxideNavigation(b *testing.B) {
  builder, runner := benchmarkFreshBuilder(b, immediateZoxide(10_000), 0)
  request := BuildRequest{Picker: protocol.PickerCD, Location: benchmarkLocation()}
  b.ReportAllocs()
  for i := 0; i < b.N; i++ {
    if _, err := builder.Build(context.Background(), request); err != nil { b.Fatal(err) }
  }
  if runner.Attempts() != b.N || runner.Starts() > b.N || runner.Exits() != runner.Starts() || runner.Processes() != runner.Starts() || runner.MaxLive() != 1 || runner.Live() != 0 { b.Fatalf("zoxide counts=%+v completed-generations=%d", runner.Counts(), b.N) }
}

func BenchmarkCPZoxideProcessCountsStayZero(b *testing.B) {
  for _, policy := range []ZoxidePolicy{ZoxideCached, ZoxideFresh} {
    builder, runner := benchmarkCPBuilder(b, policy)
    for i := 0; i < b.N; i++ {
      if _, err := builder.Build(context.Background(), benchmarkCPRequest()); err != nil { b.Fatal(err) }
    }
    if runner.Attempts() != 0 || runner.Starts() != 0 || runner.Exits() != 0 || runner.Processes() != 0 || runner.MaxLive() != 0 || runner.Live() != 0 { b.Fatalf("policy=%v zoxide counts=%+v", policy, runner.Counts()) }
  }
}
```

Run: `go test ./internal/candidate -run 'Test(Builder|Zoxide|CachedPolicy|FreshPolicy|FreshBuilder|CPNever|InitialBuilder|FreshZero|CallerCancellation|IndependentFresh)' -count=1 && go test ./internal/candidate -run '^$' -bench 'Benchmark(InitialZoxideOverlap|ZoxideTimeoutDiscard|CachedZoxideNavigation|FreshZoxideNavigation|CPZoxideProcessCountsStayZero)' -benchmem`

Expected: PASS for every parser/outcome/budget race; absolute-only zoxide rows preserve arbitrary legal bytes and relative rows soft-fail as malformed without exposing `KindVirtual` to the check; caller cancellation wins when it precedes the private timeout; exactly one invocation attempt for the default cached session with zero or one successful start and no later attempt; exactly one attempt per completed fresh `cd` generation with zero or one successful start per generation; at most one live zoxide process per session Builder even for concurrent/malicious calls; a cancelled permit waiter returns promptly without factory/attempt; independent session Builders may query concurrently; zero attempts/starts/exits/processes for `cp`; attempt 1/start 0 for missing and spawn failure; matching starts/exits/process counts, no live remainder, full partial-output discard, and owned-tree reaping. The initial-overlap benchmark uses exactly 10,000 zoxide records.

- [ ] **Step 6: Commit cached and fresh generation policies**

```bash
git add internal/candidate/zoxide.go internal/candidate/builder.go internal/candidate/zoxide_test.go internal/candidate/builder_test.go
git commit -m "feat(candidate): merge local and zoxide generations"
```

### Task 8: Add the Transactional Immutable Session Actor

**Files:**
- Create: `internal/session/types.go`
- Create: `internal/session/actor.go`
- Create: `internal/session/actor_test.go`

**Interfaces:**
- Consumes: `session.ProposedTransition` carrying complete proposed state/effect/build/rollback data and `session.GenerateFunc` returning candidate build results.
- Produces: `session.State`, `session.Snapshot`, `session.ProposedTransition`, `session.TransitionResult`, `session.GenerateFunc`, `session.New(context.Context, session.GenerateFunc) *session.Actor`, `(*session.Actor).Apply(context.Context, session.ProposedTransition) (session.TransitionResult, error)`, `(*session.Actor).Current(context.Context) (session.Snapshot, error)`, `(*session.Actor).Snapshot(context.Context, uint64) (session.Snapshot, error)`, `(*session.Actor).ResolveCurrent(context.Context, []byte) (candidate.Record, error)`, and `(*session.Actor).Close() error`.

- [ ] **Step 1: Write pending-read, atomic-publication, cancellation, immutable, and close tests**

```go
func TestActorKeepsReadsLiveAndPublishesCompleteProposalAtomically(t *testing.T) {
  generator := controllableGenerator(t)
  actor := New(context.Background(), generator.Generate)
  defer actor.Close()
  initial := proposal(0, state("/start", protocol.ModeInsert, "[I] /start/ "), build("/start"), protocol.Effect{})
  initialPending := asyncApply(actor, initial)
  generator.Complete("/start", records("start"), nil)
  initialPending.Result(t)

  pending := asyncApply(actor, proposal(1, state("/next", protocol.ModeNormal, "[N] /next/ "), build("/next"), protocol.Effect{ClearQuery: true, ClearMulti: true}))
  current, err := actor.Current(context.Background())
  if err != nil || string(current.State().Location.Path) != "/start" || current.State().Prompt != "[I] /start/ " { t.Fatalf("pending read=%+v err=%v", current, err) }
  generator.Complete("/next", records("next"), nil)
  result := pending.Result(t)
  if result.Snapshot.Generation() != 2 || result.Snapshot.State().Mode != protocol.ModeNormal || result.Snapshot.State().Prompt != "[N] /next/ " || result.Snapshot.Records()[0].Display != "next" || result.Effect.ReloadGeneration != 2 {
    t.Fatalf("result=%+v", result)
  }
}

func TestActorFailureAndMaliciousSupersedeDiscardWholeProposal(t *testing.T) {
  actor, generator := initializedActor(t, "/start")
  first := asyncApply(actor, proposal(1, state("/one", protocol.ModeNormal, "[N] /one/ "), build("/one"), protocol.Effect{}))
  second := asyncApply(actor, proposal(1, state("/two", protocol.ModeNormal, "[N] /two/ "), build("/two"), protocol.Effect{}))
  generator.AssertCancelled(t, "/one")
  first.AssertPending(t)
  second.AssertPending(t)
  generator.Complete("/one", nil, context.Canceled)
  if err := first.Err(t); !errors.Is(err, ErrSuperseded) { t.Fatalf("first=%v", err) }
  generator.AssertStarted(t, "/two")
  generator.Complete("/two", nil, fs.ErrPermission)
  if err := second.Err(t); !errors.Is(err, fs.ErrPermission) { t.Fatalf("second=%v", err) }
  current, _ := actor.Current(context.Background())
  if current.Generation() != 1 || string(current.State().Location.Path) != "/start" || current.Records()[0].Display != "start" { t.Fatalf("current=%+v", current) }
}

func TestActorNeverRollsBackCreatedTreeWhileGeneratorCanReadIt(t *testing.T) {
  actor, generator, created := actorWithCreatedTreeReader(t)
  pending := asyncApply(actor, proposalWithCreated(1, created))
  generator.WaitUntilReading(t, created.Target.Path)
  cancelApplyAndWaitForBuildCancellation(t, pending)
  created.AssertStillPresent(t)
  generator.ReleaseAndComplete(t, context.Canceled)
  if err := pending.Err(t); !errors.Is(err, context.Canceled) { t.Fatalf("apply=%v", err) }
  created.AssertRolledBack(t)
}

func TestSnapshotRecordsAreImmutableCopies(t *testing.T) {
  actor := actorWithRecords(t, records("one"))
  snap, _ := actor.Current(context.Background())
  mutated := snap.Records(); mutated[0].Display = "changed"
  again, _ := actor.Current(context.Background())
  if again.Records()[0].Display != "one" { t.Fatal("snapshot storage was mutated") }
}
```

- [ ] **Step 2: Run actor tests to verify actor symbols are absent**

Run: `go test ./internal/session -run 'TestActorKeepsReadsLiveAndPublishesCompleteProposalAtomically|TestActorFailureAndMaliciousSupersedeDiscardWholeProposal|TestSnapshotRecordsAreImmutableCopies' -count=1`

Expected: FAIL with `undefined: New` and `undefined: State`.

- [ ] **Step 3: Implement one command loop and cancellable generation completion**

```go
type State struct { Picker protocol.Picker; Mode protocol.Mode; Location pathutil.Location; Home pathutil.Location; Prompt string; AddError bool }
type GenerateFunc func(context.Context, candidate.BuildRequest) (candidate.BuildResult, error)
type Snapshot struct { generation uint64; state State; records []candidate.Record; byFullRecord map[string][]int }
type ProposedTransition struct {
  BaseGeneration uint64
  State State
  Build *candidate.BuildRequest
  Effect protocol.Effect
  Created *pathutil.CreatedTree
}
type TransitionMetrics struct { QueueWait time.Duration; TransformDuration time.Duration; Sources candidate.SourceMetrics }
type TransitionResult struct { Snapshot Snapshot; Effect protocol.Effect; Metrics TransitionMetrics }

func (s Snapshot) Generation() uint64 { return s.generation }
func (s Snapshot) State() State { return cloneState(s.state) }
func (s Snapshot) Records() []candidate.Record { return cloneRecords(s.records) }
```

Run one actor goroutine over a private command channel. `Apply` sends one command containing the complete proposal and reply channel; it accepts only `ProposedTransition`, never Task 9's `Reduction` or unresolved `AddIntent`, and no actor method calls `Current`, `Snapshot`, `Apply`, or another public actor method. Receipt of a proposal carrying `Created` transfers tree ownership to `Apply`, including stale-base, closed-actor, caller/session-cancellation, supersede, build-failure, publication-rejection, and success paths. The actor validates `BaseGeneration`, stores a pending proposal/reply/effect template/start times, and starts one generation goroutine when `Build != nil`. While pending or retiring a cancelled generation, it continues servicing `Current`, `Snapshot`, and `ResolveCurrent` against the last published snapshot. Legitimate fzf transforms are synchronous and cannot send a second mutation before the first reply.

Completion returns through the actor command channel and is the sole proof that the generator no longer reads `Build`, `CreatedTree.Target`, or any proposal-owned data. On success, require matching pending ID, live context, and unchanged base generation; build the full-record index, set the next generation, fill `Effect.ReloadGeneration`, and replace state/prompt/generation/records/index in one actor-loop assignment before replying. On ordinary build failure or publication rejection, keep every published field unchanged and perform cleanup only after that generation's completion has arrived.

On stale base or receipt after close, roll back an attached `Created` tree before replying because no generator was started. On supersede, `Apply` caller cancellation, session cancellation, or `Close` after a build starts, first cancel the pending build and mark it retiring. Do not call `Created.Rollback`, close its data, reply to its caller, or start a replacement while the generation goroutine can still read it. Wait for the matching completion command; only then roll back `Created`, reply with `ErrSuperseded` or the concrete cancellation/close error, validate the queued replacement against the still-published generation, and start it. At most one replacement waits behind the retiring generation; reject additional malicious mutations deterministically. `Close` first rejects new commands, cancels the build, waits for completion, rolls back, replies, and only then exits; it is idempotent. Tests cover stale base, supersede, caller cancel, session cancel, and close with a generator actively reading an attached target, and assert replacement start/reply/rollback ordering. A proposal with `Build == nil` atomically updates mode/prompt/effect while retaining candidate generation/records. `Snapshot(ctx, generation)` accepts only the active generation. Metrics record actor queue wait, full transform duration through reply, and candidate source timings.

- [ ] **Step 4: Verify race safety and no publication after cancellation**

Run: `go test -race ./internal/session -run 'TestActor|TestSnapshot' -count=1`

Expected: PASS with no race report or self-deadlock; read-only requests complete during a blocked/retiring build, successful proposals publish all fields together, failed/cancelled/superseded proposals publish none, and no rollback/reply/replacement occurs before generation completion.

- [ ] **Step 5: Commit the actor**

```bash
git add internal/session/types.go internal/session/actor.go internal/session/actor_test.go
git commit -m "feat(session): add transactional snapshot actor"
```

### Task 9: Implement Modes, Navigation, Add Creation, and Selection Validation

**Files:**
- Create: `internal/session/events.go`
- Create: `internal/session/selection.go`
- Create: `internal/session/events_test.go`
- Create: `internal/session/selection_test.go`
- Modify: `internal/session/actor.go`
- Modify: `internal/session/types.go`

**Interfaces:**
- Consumes: `protocol.Event`, active full-record identity, actor state, `pathutil` root/home/parent behavior, and published candidate generations.
- Produces: immutable `session.AddIntent`, exclusive `session.Reduction`, `session.ErrInvalidBase`, `session.Reduce(session.Snapshot, protocol.Event) (session.Reduction, error)`, `session.Handle(context.Context, *session.Actor, protocol.Event) (session.TransitionResult, error)`, `session.ValidateCD(session.Snapshot, [][]byte) (protocol.Outcome, error)`, and `session.ValidateCP(session.Snapshot, [][]byte, []byte) (protocol.Outcome, error)`.

- [ ] **Step 1: Write the complete mode and query transition table as failing tests**

```go
func TestModeTransitions(t *testing.T) {
  actor := readyActor(t, protocol.PickerCP, "/work")
  tests := []struct{ event protocol.Event; mode protocol.Mode; clear, accept, ignore bool; put string }{
    {protocol.Event{Opcode: protocol.OpEscape, Key: "esc"}, protocol.ModeNormal, false, false, false, ""},
    {protocol.Event{Opcode: protocol.OpModeAdd, Key: "a"}, protocol.ModeAdd, false, false, false, ""},
    {protocol.Event{Opcode: protocol.OpSlash, Key: "/", Query: []byte("x")}, protocol.ModeAdd, false, false, false, "/"},
    {protocol.Event{Opcode: protocol.OpHome, Key: "~", Query: []byte("x")}, protocol.ModeAdd, false, false, false, "~"},
  }
  for i, tc := range tests {
    result, err := Handle(context.Background(), actor, tc.event)
    effect := result.Effect
    if err != nil || effect.Mode != tc.mode || effect.ClearMulti != tc.clear || effect.Accept != tc.accept || effect.Ignore != tc.ignore || effect.Put != tc.put {
      t.Fatalf("case=%d effect=%+v err=%v", i, effect, err)
    }
  }
}

func TestSlashMatrix(t *testing.T) {
  cases := []struct{ mode protocol.Mode; query string; want effectCheck }{
    {protocol.ModeAdd, "..", effectCheck{put: "/"}},
    {protocol.ModeNormal, "name", effectCheck{ignore: true}},
    {protocol.ModeInsert, "name", effectCheck{put: "/"}},
    {protocol.ModeInsert, "..", effectCheck{location: "/parent", reload: true, clearQuery: true}},
    {protocol.ModeNormal, "", effectCheck{location: "/", reload: true, clearQuery: true}},
  }
  runSlashCases(t, cases)
}

func TestNormalEscapeHasOnlyClearMultiEffect(t *testing.T) {
  actor := readyActorInMode(t, protocol.PickerCP, "/work", protocol.ModeNormal)
  before, _ := actor.Current(context.Background())
  result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEscape, Key: "esc"})
  if err != nil { t.Fatal(err) }
  if !reflect.DeepEqual(result.Effect, protocol.Effect{ClearMulti: true}) { t.Fatalf("effect=%+v", result.Effect) }
  if !reflect.DeepEqual(result.Snapshot.State(), before.State()) || result.Snapshot.State().Mode != protocol.ModeNormal { t.Fatalf("result=%+v", result) }
}

func TestReduceValidAddIsPureAndDiscardNeedsNoRollback(t *testing.T) {
  base := t.TempDir()
  blockingFile := filepath.Join(base, "existing-file")
  if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil { t.Fatal(err) }
  snapshot := snapshotInAddMode(t, pathutil.Filesystem([]byte(base)))
  event := protocol.Event{Opcode: protocol.OpEnter, Query: []byte("existing-file/child")}
  reduction, err := Reduce(snapshot, event)
  if err != nil || !reduction.hasAddIntent() || reduction.hasProposal() { t.Fatalf("reduction=%+v err=%v", reduction, err) }
  event.Query[0] = 'X'
  copied := reduction.addIntentForTest()
  copied.query[0], copied.base.Path[0] = 'Y', 'Z'
  again := reduction.addIntentForTest()
  if string(again.query) != "existing-file/child" || string(again.base.Path) != base { t.Fatalf("intent=%+v", again) }
  discarded, err := Reduce(snapshot, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("missing/child")})
  if err != nil || !discarded.hasAddIntent() { t.Fatalf("discarded=%+v err=%v", discarded, err) }
  if _, err := os.Lstat(filepath.Join(base, "missing")); !errors.Is(err, fs.ErrNotExist) { t.Fatalf("unexpected path: %v", err) }
}

func TestHandleAddCreatesOnceAndAppliesOnce(t *testing.T) {
  actor, generator := readyCountingActorInAddMode(t, protocol.PickerCD, t.TempDir())
  result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("new/child")})
  if err != nil { t.Fatal(err) }
  generator.AssertOneBuild(t)
  assertOneCreatedTree(t, result.Snapshot.State().Location, "new/child")
  if result.Snapshot.State().Mode != protocol.ModeNormal { t.Fatalf("result=%+v", result) }
}

func TestHandleAddCreateErrorHasOnlyErrorEffect(t *testing.T) {
  base := t.TempDir()
  if err := os.WriteFile(filepath.Join(base, "taken"), []byte("x"), 0o600); err != nil { t.Fatal(err) }
  actor, generator := readyCountingActorInAddMode(t, protocol.PickerCD, base)
  before, _ := actor.Current(context.Background())
  result, err := Handle(context.Background(), actor, protocol.Event{Opcode: protocol.OpEnter, Query: []byte("taken/child")})
  if err != nil { t.Fatal(err) }
  want := protocol.Effect{Prompt: addErrorPrompt(before.State().Location), ErrorPrompt: true}
  if !reflect.DeepEqual(result.Effect, want) { t.Fatalf("effect=%+v", result.Effect) }
  generator.AssertNoBuild(t)
  assertRetainedAddSnapshot(t, before, result.Snapshot)
  if got, err := os.ReadFile(filepath.Join(base, "taken")); err != nil || string(got) != "x" { t.Fatalf("file=%q err=%v", got, err) }
}
```

Write separate tests before implementation for: Insert Enter and Normal Enter acceptance; an unknown nonempty current-item on Enter rejecting rather than falling through to acceptance; Enter on `KindVirtual` navigating to its authoritative Drives target instead of accepting; Insert Esc preserving marks; Normal Esc producing exactly `protocol.Effect{ClearMulti: true}` while retaining Normal/current state; Add Esc entering Normal without clearing marks; `i` entering Insert; `/` root and exact `..`; `~` Home; POSIX root clamp, Windows drive-root parent, UNC-share-root parent, Drives, non-root filesystem parent, and Home variants; `cd` forward on every directory-capable active record including `virtual`; `cp` forward only on `directory`, `drive`, or `virtual`; mark clearing on every navigation; Add success and all lexical invalid cases; Add in Drives; a valid Add reduction returning an intent without inspecting or creating filesystem state; cloned Add query/base bytes remaining unchanged after caller mutation; real `Handle` Add creation followed by exactly one build/apply path; create/check errors producing only the `[A!]` error-prompt effect while retaining Add mode/location/generation/records/query; Unix filename-backslash prompt escaping; one-visible-backslash Windows `C:\` and `Drives\` prompts; fzf grammar escaping of those prompt values; unknown full records; one `cd` filesystem selection; `cd` virtual final-selection rejection; `cp` visible-order restoration; duplicate multiplicity; rejection when any `cp` mark is virtual; Windows cross-volume `cp` absolute fallback; both nil and empty `ValidateCP` base rejecting with `ErrInvalidBase` before relative conversion; Add query symlink/reparse rejection; Unix symlink-base-ancestor rejection; Windows junction-base-ancestor rejection; partial-create and existing-file errors; actual Handle-created tree rollback on generation failure, caller cancellation, session cancellation, stale base, malicious supersede, and close; rollback only after generator completion; and preservation of every pre-existing parent.

- [ ] **Step 2: Run event and selection tests to verify behavior is absent**

Run: `go test ./internal/session -run 'Test(Mode|NormalEscape|Reduce|Handle|Slash|Home|Forward|Parent|Enter|Add|Prompt|Validate)' -count=1`

Expected: FAIL with `undefined: Handle` and `undefined: ValidateCP`.

- [ ] **Step 3: Implement the exact semantic transition matrix**

| Event | Insert | Normal | Add |
|---|---|---|---|
| `mi` | reject because `i` is unbound | Insert, search on | reject because `i` is unbound |
| `ma` | reject because `a` is unbound | Add, search on, clear query | reject because `a` is unbound |
| `es` | Normal, search off, preserve marks | remain Normal, clear marks only | Normal, search off, clear query, preserve marks |
| `fw` | navigate selected directory | navigate selected directory | ignore |
| `up` | navigate parent | navigate parent | ignore |
| `sl` with empty query | root/Drives | root/Drives | `put(/)` |
| `sl` with exact `..` | parent | ignore because query is nonempty | `put(/)` |
| `sl` with any other nonempty query | `put(/)` | ignore | `put(/)` |
| `hm` with empty query | Home | Home | `put(~)` |
| `hm` with nonempty query | `put(~)` | ignore | `put(~)` |
| `en` | accept, except navigate an exact current `KindVirtual` record to Drives | accept, except navigate an exact current `KindVirtual` record to Drives | validate, create, navigate, then Normal |

Define an opaque immutable `AddIntent` with private `baseGeneration uint64`, `base pathutil.Location`, and `query []byte`, and an opaque `Reduction` with private mutually exclusive `proposal *ProposedTransition` and `add *AddIntent`. Constructors enforce exactly one branch; Add construction deep-clones `base.Path` and `query`, and every test/accessor returns clones. `Reduce(Snapshot, Event)` returns this type. It may resolve exact records, validate modes, run `ValidateAddQuery`, derive lexical locations, and build prompts/effects, but it performs no `Lstat`, mkdir, rollback, filesystem call, actor call, goroutine start, or mutation of snapshot/event/caller-owned bytes. Ordinary events and lexically invalid Add/Drives return a complete proposal. A lexically valid Add returns only `AddIntent`; discarding it has no side effect and requires no rollback. Normal Escape is the complete current Normal state with exactly `protocol.Effect{ClearMulti: true}`; every other effect field is its zero value.

`Handle` is a standalone orchestration function, not an actor method. It reads exactly one immutable snapshot, calls `Reduce` exactly once, and selects its one branch. An ordinary proposal goes directly to the single `Actor.Apply` call. For `AddIntent`, `Handle` calls `pathutil.CreateDirectoryTree(intent.base, intent.query)` exactly once. A create/check error, including partial-create cleanup or an existing file, becomes a complete no-build proposal with the snapshot's Add mode/location/home/generation/records retained, `[A!]` prompt and `AddError` set, query left untouched, and exactly `protocol.Effect{Prompt: errorPrompt, ErrorPrompt: true}`; `Handle` still calls `Actor.Apply` exactly once. Success becomes a complete Normal target-location/prompt/build/reload-clear-select-first proposal with `CreatedTree` attached. Keep a local rollback guard while preparing that proposal: any error or cancellation after creation but before invoking `Actor.Apply` rolls back. Invoking `Actor.Apply` transfers exclusive ownership, so `Handle` never rolls back afterward; stale base, rejection after close, build failure, caller/session cancellation, supersede, and close are actor-owned and obey cancel-then-generation-complete-then-rollback ordering. No unresolved `AddIntent` can reach `Actor.Apply`, and no branch calls `Apply` twice.

Build prompts through platform-aware `pathutil.PromptDisplay` and prefix `[I]`, `[N]`, `[A]`, or `[A!]`: POSIX root remains `/ ` rather than `// `; Unix filename backslashes are visibly escaped; Windows native separators remain one visible backslash, so drive root is `C:\ ` and the virtual location is `Drives\ `. `fzf.changePrompt` preserves that display string literally as the terminal colon-delimited fzf action, without changing its eventual visible separators or allowing delimiter-looking prompt text to become another action. Effects use `CursorLine` for Insert/Add and `CursorBlock` for Normal except for the exact Normal Escape effect above. Every successful navigation proposes complete Normal/mode/location/prompt/build/effect state, clears marks/query, waits synchronously for publication, selects first, and receives the published generation. Directory-capable navigation kinds are exactly `local|directory|zoxide|drive|virtual`; `KindVirtual` navigates its authoritative `Record.Target`, which must be `pathutil.Drives()`, rather than decoding its payload as a path. Forward rejects an empty current item, a full record absent from the active snapshot, and a `cp` file. Enter normally requests acceptance, but an unknown nonempty current item is rejected; when the exact active current record is `KindVirtual`, Enter performs the same synchronous navigation to Drives and does not accept/close because Drives has no filesystem path. Add and navigation prompt tests cover POSIX root, Unix filename backslash, drive root, UNC share root, Drives, non-root filesystem paths, trailing separators, invalid POSIX bytes, Home, and every fzf action delimiter.

- [ ] **Step 4: Implement exact full-record selection matching**

```go
func ValidateCP(s Snapshot, accepted [][]byte, base []byte) (protocol.Outcome, error) {
  if len(base) == 0 { return protocol.Outcome{}, ErrInvalidBase }
  counts := make(map[string]int, len(accepted))
  for _, raw := range accepted {
    if _, err := protocol.ParseRecord(raw); err != nil { return protocol.Outcome{}, ErrInvalidSelection }
    counts[string(raw)]++
  }
  paths := make([][]byte, 0, len(accepted))
  for _, record := range s.records {
    key := record.FullKey()
    if counts[key] > 0 {
      if record.Kind == protocol.KindVirtual || record.Target.Kind != pathutil.KindFilesystem { return protocol.Outcome{}, ErrInvalidSelection }
      paths = append(paths, pathutil.Relative(base, record.Path))
      counts[key]--
    }
  }
  if len(paths) != len(accepted) { return protocol.Outcome{}, ErrUnknownSelection }
  return protocol.Outcome{Status: protocol.StatusAccepted, Paths: paths}, nil
}
```

`ValidateCD` requires exactly one accepted record, resolves that exact full record once in the active snapshot, requires a filesystem directory-capable kind and authoritative `KindFilesystem` target, rejects `KindVirtual`, and returns its absolute `Record.Path`. `ValidateCP` rejects both nil and empty original-base slices with `ErrInvalidBase`, then requires at least one record, validates exactly two tabs and canonical nonempty payload syntax, counts accepted full records, iterates the snapshot in visible order, preserves duplicate multiplicity, rejects the complete acceptance if any matched record is `KindVirtual` or otherwise lacks a filesystem target, rejects any residual count, and computes output relative to the original invocation directory rather than the navigated directory. When `filepath.Rel` cannot cross Windows volumes, output is the unchanged absolute target. Thus `cp` marks containing a virtual root-parent record cannot be accepted. A valid payload in an unknown display/kind record is rejected; full wire identity, not decoded payload bytes, remains authorization.

- [ ] **Step 5: Verify all semantic tests and races**

Run: `go test -race ./internal/session -count=1`

Expected: PASS; all three modes, exact Normal Escape, pure/cloned Add reductions, one-create/one-apply Handle orchestration, actual Handle-created ownership and rollback, exact identity, order, multiplicity, nil/empty base rejection, cross-volume fallback, unknown Enter rejection, virtual Enter/forward navigation to Drives, virtual preview/final-output rejection boundaries, and every platform root variant pass without races.

- [ ] **Step 6: Commit session behavior**

```bash
git add internal/session
git commit -m "feat(session): implement modal navigation and selection"
```

### Task 10: Add Bearer-Authenticated Ephemeral Loopback IPC

**Files:**
- Create: `internal/sessionipc/dto.go`
- Create: `internal/sessionipc/auth.go`
- Create: `internal/sessionipc/server.go`
- Create: `internal/sessionipc/client.go`
- Create: `internal/sessionipc/sessionipc_test.go`

**Interfaces:**
- Consumes: a `sessionipc.Backend`, one bearer token, exact base64 DTOs, and an ephemeral IPv4 loopback listener.
- Produces: `sessionipc.Token`, `sessionipc.NewToken() (Token, error)`, `(sessionipc.Token).String() string`, `sessionipc.Listen(context.Context, Token, Backend) (*Server, error)`, `(*Server).Address() string`, `(*Server).Close(context.Context) error`, `sessionipc.NewClientFromEnv(func(string) string) (*Client, error)`, `(*Client).Event`, `(*Client).Load`, `(*Client).ResolvePreview`, and `(*Client).RecordPreview`.

- [ ] **Step 1: Write exact bearer/route, request/response-limit, concurrency, and cooperative ordered-close tests**

```go
func TestServerRejectsForgedBearerAndLeaksNoToken(t *testing.T) {
  server, client := startTestServer(t)
  req := validEventRequest(t, protocol.OpEscape)
  if _, err := client.Event(context.Background(), req); err != nil { t.Fatal(err) }
  client.token = fixedToken(9)
  if _, err := client.Event(context.Background(), req); !errors.Is(err, ErrUnauthorized) { t.Fatalf("bearer=%v", err) }
  if strings.Contains(server.Logs(), server.token.String()) { t.Fatal("token entered logs") }
}

func TestClientAcceptsOnlyExactLoopbackURL(t *testing.T) {
  accepted := []string{"http://127.0.0.1:1", "http://127.0.0.1:65535"}
  rejected := []string{"https://127.0.0.1:1", "http://localhost:1", "http://127.0.0.2:1", "http://user@127.0.0.1:1", "http://127.0.0.1:0", "http://127.0.0.1:1/", "http://127.0.0.1:1/x", "http://127.0.0.1:1?q=1", "http://127.0.0.1:1#x"}
  for _, raw := range accepted { if _, err := parseEndpoint(raw); err != nil { t.Fatalf("rejected %q: %v", raw, err) } }
  for _, raw := range rejected { if _, err := parseEndpoint(raw); err == nil { t.Fatalf("accepted %q", raw) } }
}

func TestServerLimitsRequestsAndConcurrentHandlers(t *testing.T) {
  server, client := startBlockingServer(t)
  assertStatus(t, client.rawChunked(bytes.NewReader(bytes.Repeat([]byte{'x'}, 64<<10+1))), http.StatusRequestEntityTooLarge)
  release := occupyHandlers(t, client, 16)
  assertStatus(t, client.Event(context.Background(), validEventRequest(t, protocol.OpEscape)), http.StatusTooManyRequests)
  close(release)
  closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second); defer cancel()
  if err := server.Close(closeCtx); err != nil { t.Fatal(err) }
  if _, err := client.Event(context.Background(), validEventRequest(t, protocol.OpEscape)); err == nil { t.Fatal("closed endpoint accepted request") }
  server.AssertListenerClosed(t)
  server.AssertHandlersCancelledAndJoined(t)
}

func TestPreviewTelemetryUsesIndependentSoft250msTimeout(t *testing.T) {
  server, client := startTelemetryServerThatNeverReplies(t)
  defer server.ForceClose()
  cancelled, cancel := context.WithCancel(context.Background()); cancel()
  started := time.Now()
  if err := client.RecordPreview(cancelled, startedPreviewRequest()); err != nil { t.Fatalf("soft telemetry err=%v", err) }
  if elapsed := time.Since(started); elapsed > 300*time.Millisecond { t.Fatalf("telemetry blocked %v", elapsed) }
  server.AssertRequestObserved(t)
}

func TestServerCloseCancelsCooperativeBackendAndJoinsHandlers(t *testing.T) {
  backend := newContextBlockedBackend()
  server, client := startServer(t, backend)
  requestDone := make(chan error, 1)
  go func() { _, err := client.Event(context.Background(), validEventRequest(t, protocol.OpEscape)); requestDone <- err }()
  backend.WaitUntilCalled(t)
  closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second); defer cancel()
  if err := server.Close(closeCtx); err != nil { t.Fatal(err) }
  backend.AssertReturnedAfterContextCancellation(t)
  server.AssertHandlersCancelledAndJoined(t)
  select { case <-requestDone: case <-closeCtx.Done(): t.Fatal("request handler did not join within close bound") }
}
```

Add table tests that send each server target through a raw loopback request. Accept only the three literal paths; reject `/v1/event?x=1`, `/v1%2fevent`, `/v1/event/`, `//v1/event`, `/v1//event`, and alternate percent-escaped spellings before any backend method is called. Add raw-header cases for missing authorization, duplicate valid headers, valid-plus-invalid duplicates in either order, one comma-joined value, `bearer`, alternate scheme, leading/trailing whitespace, and doubled whitespace; only one exact `Authorization: Bearer <token>` field/value reaches constant-time comparison and the backend.

Use a rogue loopback server to test all client response classes at exactly their limit and at limit plus one: event JSON, resolving-preview JSON, JSON error, load octet stream, and telemetry empty response. Construct exact-limit valid JSON with bounded padding/streaming and stream the 64 MiB load case rather than retaining duplicate large buffers. Each at-limit response is accepted when otherwise valid; each limit-plus-one response is rejected before data is returned, its body is closed, and an unread overlimit connection is not reused. Separately make finished telemetry requests with an authorized current item and otherwise-valid required renderer, duration, and outcome fields while varying only `child_starts` and `max_live_children`, so `3/1` reaches and passes the intended bound and `4/1` plus `3/2` reach and fail that validation. Make the preview backend return an actual `protocol.ResolvedCandidate{Kind: protocol.KindVirtual}` for an authorized record and assert rejection before response data is returned.

- [ ] **Step 2: Run IPC tests to verify server and client are missing**

Run: `go test ./internal/sessionipc -run 'TestServer' -count=1`

Expected: FAIL with `undefined: Listen` and `undefined: NewToken`.

- [ ] **Step 3: Define exact DTOs and backend routes**

```go
type Token [32]byte
func (t Token) String() string { return base64.RawURLEncoding.EncodeToString(t[:]) }

type EventRequest struct {
  Opcode protocol.Opcode `json:"opcode"`
  Key string `json:"key"`
  QueryBase64 string `json:"query_base64"`
  CurrentItemBase64 string `json:"current_item_base64"`
}
type EventResponse struct { Effect protocol.Effect `json:"effect"` }
type LoadRequest struct { Generation uint64 `json:"generation"` }
type PreviewRequest struct {
  Phase string `json:"phase"`
  CurrentItemBase64 string `json:"current_item_base64"`
  Renderer string `json:"renderer,omitempty"`
  DurationUS int64 `json:"duration_us,omitempty"`
  ChildStarts int `json:"child_starts,omitempty"`
  MaxLiveChildren int `json:"max_live_children,omitempty"`
  Outcome string `json:"outcome,omitempty"`
}
type PreviewResponse struct {
  Kind protocol.Kind `json:"kind"`
  PathBase64 string `json:"path_base64"`
  Size int64 `json:"size"`
  ModTimeUnixNano int64 `json:"mod_time_unix_nano"`
  Mode uint32 `json:"mode"`
}
type Backend interface {
  HandleEvent(context.Context, protocol.Event) (protocol.Effect, error)
  LoadGeneration(context.Context, uint64) ([]byte, error)
  ResolvePreview(context.Context, []byte) (protocol.ResolvedCandidate, error)
  RecordPreview(context.Context, PreviewRequest) error
}
```

The `Backend` interface contract requires every method to observe its request context promptly and return after cancellation. The production actor-backed implementation in Task 13 satisfies that contract. Server handlers call backend methods directly in their tracked handler goroutines; they never detach a backend call into an untracked goroutine. A deliberately cancellation-ignoring backend violates the interface precondition and is not required to be reclaimed because Go cannot forcibly terminate the call without a goroutine leak.

Serve only `POST /v1/event`, `POST /v1/load`, and `POST /v1/preview`. Before authorization or backend invocation, require `RequestURI` to equal exactly one of those three literal paths and require empty `RawQuery` and `RawPath`; reject query, encoded slash/path, trailing slash, duplicate slash, and every alternate escaped spelling without routing through normalized `URL.Path`. JSON requests use `DisallowUnknownFields`, require one object followed by EOF, and decode all byte-bearing fields through strict padded base64. Preview phase is exactly `resolve`, `started`, or `finished`: `resolve` requires only the current item, resolves exact current-snapshot full-record membership, and rejects an actual backend-returned `KindVirtual` or any non-filesystem target rather than returning its token as `PathBase64`; `started` and `finished` revalidate the same previewable current membership, reject controls in renderer/outcome, record bounded nonnegative telemetry, and return HTTP 204. Finished telemetry with otherwise-valid required fields may report `child_starts` in `0..3` and `max_live_children` in `0..1`, with zero for native fallback; it never conflates sequential starts with simultaneous children. No preview DTO carries or claims a generation. Started/finished client calls each use an independent soft context capped at 250 ms; their timeout/error never changes preview rendering. A callback killed with its inherited renderer group may send no `finished` call, and the parent must not synthesize one. `/v1/load` responds `application/octet-stream`; event and resolving preview requests respond JSON. Map bearer failure to 401, body size to 413, handler saturation to 429, unknown/non-previewable current identity or load generation to 404, invalid DTO/event to 400, and internal failure to 500 without internal details.

- [ ] **Step 4: Implement one token, strict endpoint parsing, limits, and ordered shutdown**

Generate exactly 32 token bytes with `crypto/rand.Read` and encode raw URL-safe. Require `len(request.Header["Authorization"]) == 1`, one field/value, the exact case-sensitive `Bearer ` prefix, exact expected total length, and no alternate whitespace, scheme, comma, or joined value before comparing the complete `"Bearer "+token.String()` bytes with `subtle.ConstantTimeCompare`; malformed grammar and duplicates never reach that comparison or the backend. There is no exposed trace identifier, secondary authentication header/state, or credential route. Listen using `net.Listen("tcp4", "127.0.0.1:0")`; publish `fmt.Sprintf("http://127.0.0.1:%d", listenerPort)`. `parseEndpoint` requires scheme `http`, host literal `127.0.0.1`, decimal port `1..65535`, empty userinfo/path/raw-path/query/fragment, and no normalization to another spelling.

Configure `ReadHeaderTimeout: 1s`, `ReadTimeout: 2s`, `WriteTimeout: 0`, `IdleTimeout: 15s`, and `MaxHeaderBytes: 8<<10`; wrap bodies with `http.MaxBytesReader(responseWriter, request.Body, 64<<10)`, including unknown-length chunked bodies, and reject byte 64 KiB plus one. Acquire a buffered semaphore of 16 before decoding and track every admitted handler in a `WaitGroup`. Client transport uses a 150 ms dial timeout, no proxy, and no redirects. Define exact constants `maxJSONResponseBytes = 64 << 10`, `maxLoadResponseBytes = 64 << 20`, and `maxTelemetryResponseBytes = 1 << 10`. Event, resolving-preview, every non-success JSON error, load/octet-stream, and started/finished telemetry response paths read, decode, or drain through the applicable limit plus one, reject overlimit before returning any data, always close `response.Body`, and prevent connection reuse after overlimit as appropriate. The event route uses only its caller/session context and derives no independent deadline: cached navigation reads the immutable session zoxide cache without starting or waiting for zoxide, while fresh navigation performs its generation-local query under the candidate budget within that same synchronous event context. Load and preview-resolution methods derive their own 10-second request contexts. `Server.Address` returns only the validated loopback URL; it does not build an environment. Task 11 passes address and token through dedicated controlled `fzf.Config` fields after inherited-environment sanitization. Access logs contain route/status only.

`Server.Close(ctx)` atomically marks closing so middleware rejects new work, cancels the HTTP base and active handler contexts, closes the listener, performs bounded `http.Server.Shutdown` and then `http.Server.Close` as needed within at most the caller's two-second deadline, waits for the tracked handler `WaitGroup`, and returns. Under the cooperative `Backend` precondition, every backend call returns after cancellation, so all listener, connection, and handler goroutines join within the bound. Do not detach backend calls or claim that `Close` can reclaim a backend that ignores cancellation. Actor closure is deliberately outside this method and occurs afterward in Task 13.

- [ ] **Step 5: Verify race, leak, and transport tests**

Run: `go test -race ./internal/sessionipc -count=1`

Expected: PASS with no races; only one exact Authorization field/value is accepted; noncanonical route targets are rejected before backend invocation; malformed client endpoint variants are rejected; chunked request byte 64 KiB plus one returns 413; the 17th handler is refused; event cancellation follows its caller; an actual backend-returned `KindVirtual` preview is rejected; otherwise-valid finished telemetry reaches exact `0..3`/`0..1` bounds; JSON event/preview/error responses accept 64 KiB and reject 64 KiB plus one, load accepts 64 MiB and rejects 64 MiB plus one, telemetry accepts/drains 1 KiB and rejects 1 KiB plus one, all bodies close, and overlimit connections are not reused. Telemetry still returns or soft-times out within 250 ms, and cancellation of a context-blocked cooperative backend makes it return so listener/idle connections/all tracked handler goroutines are gone after bounded `Close`; no cancellation-ignoring backend reclamation is required.

- [ ] **Step 6: Commit IPC**

```bash
git add internal/sessionipc
git commit -m "feat(ipc): add bearer-authenticated loopback transport"
```

### Task 11: Build Typed fzf Actions, Bindings, Launch, and NUL Output Parsing

**Files:**
- Create: `internal/fzf/action.go`
- Create: `internal/fzf/options.go`
- Create: `internal/fzf/process.go`
- Create: `internal/fzf/output.go`
- Create: `internal/fzf/action_test.go`
- Create: `internal/fzf/options_test.go`
- Create: `internal/fzf/process_test.go`
- Create: `internal/fzf/output_test.go`

**Interfaces:**
- Consumes: `protocol.Effect`, picker, initial prompt/generation/input, executable absolute path, process runner, fzf path, inherited environment, and explicit callback address/token controls.
- Produces: `fzf.RenderEffect(protocol.Effect) (string, error)`, `fzf.Options(protocol.Picker, string) []string`, `fzf.Run(context.Context, fzf.Config) (fzf.Result, error)`, `fzf.ParseOutput(protocol.Picker, []byte, int) (fzf.Result, error)`, and `fzf.CheckVersion(context.Context, process.Runner, string) error`.

- [ ] **Step 1: Write exact action and option golden tests**

```go
func TestRenderNavigationEffect(t *testing.T) {
  got, err := RenderEffect(protocol.Effect{Mode: protocol.ModeNormal, Prompt: `[N] a\)b/ `, ClearMulti: true, ClearQuery: true, ReloadGeneration: 7})
  want := `clear-multi+reload-sync(l:7)+clear-query+wait+first+change-prompt:[N] a\)b/ `
  if err != nil || got != want { t.Fatalf("got=%q want=%q err=%v", got, want, err) }
}

func FuzzActionArgumentsRejectInjection(f *testing.F) {
  for _, seed := range []string{"ok", "x)+execute(id)", "x\rdown", "x\naccept", "x\x00y", `x\\)+reload(e:en)`} { f.Add(seed) }
  f.Fuzz(func(t *testing.T, raw string) {
    action, err := changePrompt(raw)
    if strings.ContainsAny(raw, "\r\n\x00") {
      if err == nil { t.Fatalf("accepted control input %q as %q", raw, action.text) }
      return
    }
    if err == nil { assertTerminalPromptAction(t, action.text, raw) }
  })
}

func TestPickerOptions(t *testing.T) {
  cd := Options(protocol.PickerCD, "[I] /work/ ")
  cp := Options(protocol.PickerCP, "[I] /work/ ")
  assertContainsExactly(t, cd, "--multi=1", "--sort", "--print-query", "--read0", "--print0")
  assertContainsExactly(t, cp, "--multi", "--no-sort", "--read0", "--print0")
  assertSingleFZFBindings(t, cd, cp)
  assertNoListen(t, cd, cp)
}

func TestWindowsPromptBackslashIsPreservedByTerminalAction(t *testing.T) {
  got, err := changePrompt(`[N] C:\ `)
  if err != nil { t.Fatal(err) }
  assertFZFDisplaysPrompt(t, got.text, `[N] C:\ `)
}

func TestConfigSeparatesCallbackCredentialsFromInheritedEnvironment(t *testing.T) {
  config := testConfig()
  config.Environment = []string{"PATH=/bin", "SHELL_PICKER_ADDR=http://forged", "SHELL_PICKER_TOKEN=forged"}
  config.CallbackAddress = "http://127.0.0.1:4321"
  config.CallbackToken = "controlled-token"
  child := runWithRecordingRunner(t, config)
  assertEnvExactlyOnce(t, child.Env, "SHELL_PICKER_ADDR", config.CallbackAddress)
  assertEnvExactlyOnce(t, child.Env, "SHELL_PICKER_TOKEN", config.CallbackToken)
}

func TestRunStartsOnlySessionFZFAndDoesNotProbeVersion(t *testing.T) {
  runner := recordingRunner(t, successfulFZFResult())
  config := testConfig()
  config.Runner = runner
  if _, err := Run(context.Background(), config); err != nil { t.Fatal(err) }
  calls := runner.Calls()
  if len(calls) != 1 || slices.Contains(calls[0].Args, "--version") { t.Fatalf("calls=%+v", calls) }
}

func TestInstalledFZFCheckVersion(t *testing.T) {
  path := os.Getenv("SHELL_PICKER_REAL_FZF")
  if path == "" { t.Skip("SHELL_PICKER_REAL_FZF is required for the installed-version gate") }
  if err := CheckVersion(context.Background(), process.Runner{}, path); err != nil { t.Fatal(err) }
}
```

- [ ] **Step 2: Run fzf tests to verify typed rendering is absent**

Run: `go test ./internal/fzf -run 'TestRender|TestPickerOptions|TestRun|TestParse' -count=1`

Expected: FAIL with `undefined: RenderEffect` and `undefined: Options`.

- [ ] **Step 3: Implement a closed typed action vocabulary**

```go
type action struct { text string }
func sequence(actions ...action) string
func enableSearch() action
func disableSearch() action
func rebind(mode protocol.Mode) action
func unbind(mode protocol.Mode) action
func clearQuery() action
func clearMulti() action
func reload(generation uint64) action
func changePrompt(prompt string) (action, error)
func put(text string) (action, error)
func acceptEnter() action
func ignore() action
func wait() action
func first() action
```

No constructor accepts raw action text. `changePrompt`, `put`, and every argument constructor reject CR, LF, and NUL. `changePrompt` uses fzf 0.74.1's colon form and is always terminal: its exact text is `change-prompt:` followed by the unchanged display string, so fzf masks all remaining bytes as one argument rather than interpreting backslashes or delimiter-looking text. `put` accepts only `/` or `~` and may use its fixed parenthesized form. This grammar handling is distinct from `PromptDisplay`: one Windows separator backslash remains one visible backslash, while an escaped Unix filename backslash retains its intended visible escaping. Fuzz seeds and a fixed delimiter corpus cover `)+`, commas, colons, backslashes, substitutions, transform text, and action names and prove terminal prompt text cannot inject a second action. The installed-fzf gate exercises Windows and Unix backslashes plus the adversarial delimiter corpus against real 0.74.1 semantics, not a custom parser. Render Insert as search enabled, navigation keys rebound, and Normal keys unbound; Normal as search disabled with all modal/navigation keys rebound; Add as search enabled with all modal/navigation keys unbound and query cleared. Set `Effect.Cursor` to line for Insert/Add and block for Normal; Task 12 applies it outside action stdout. Navigation sequences `clearMulti()`, `reload(effect.ReloadGeneration)`, `clearQuery()`, `wait()`, `first()`, and terminal `changePrompt(effect.Prompt)` in that authoritative order. Acceptance renders `print(enter)+accept`; Normal Esc renders only `clear-multi`; put and ignore are single typed actions.

- [ ] **Step 4: Build exact common and picker-specific fzf arguments**

Common arguments are `--ansi`, `--style=full`, `--layout=reverse`, an argument formed as `--delimiter=` plus one literal tab byte, `--with-nth=2`, `--read0`, `--print0`, prompt, preview `p`, and `--preview-window=right:50%:wrap`. Bind `enter:transform(e:en)`, `esc:transform(e:es)`, `i:transform(e:mi)`, `a:transform(e:ma)`, `ctrl-l,tab,right:transform(e:fw)`, `ctrl-h,left:transform(e:up)`, `/:transform(e:sl)`, `~:transform(e:hm)`, `j:down`, `k:up`, `h:trigger(ctrl-h)`, `l:trigger(tab)`, and `q:abort`. Start unbinds `h,j,k,l,i,a,q,space`. For `cd`, bind `space:clear-multi+toggle`, sort, print query, and limit multi to one. For `cp`, bind `space:toggle`, preserve candidate order with no-sort, and allow unrestricted multi. `Options` obtains every action segment, including static transform/trigger/toggle actions, from constructors in `action.go`; no other production file concatenates fzf action grammar.

- [ ] **Step 5: Launch by basename and parse exact NUL result shapes**

```go
type Config struct {
  Picker protocol.Picker
  FZFPath string
  ExecutablePath string
  Environment []string
  CallbackAddress string
  CallbackToken string
  Options []string
  Input []byte
  Runner process.Runner
  ForegroundTTY *os.File
  TTYOut io.Writer
  TTYErr io.Writer
}
type Result struct { Query []byte; Key string; Records [][]byte; Aborted bool; ExitCode int }
```

`CheckVersion` parses direct `fzf --version` output, uses `ContainmentOwnTree` plus `process.SanitizeEnv`, and rejects versions below 0.74.1. fzf 0.74.1 or newer is an external installation/runtime precondition: `Run` deliberately does not verify it, behavior with older fzf is unsupported, and probe, CI, and real-fzf gates call `CheckVersion` outside normal sessions. A normal `Run` performs no version preflight and starts exactly one process, the session fzf. Require `filepath.Base(ExecutablePath)` to match `^[A-Za-z0-9._-]+$`; reject empty, whitespace, separators, quotes, shell metacharacters, and control bytes before process start. Reject an executable directory containing `os.PathListSeparator` because it cannot be prepended to `PATH` as one entry. A directory containing spaces remains supported because only the basename enters `--with-shell`.

Set the session option to `"--with-shell=" + basename + " --fzf-shell"`. Require `CallbackAddress` and `CallbackToken` as nonempty dedicated fields and never infer either from `Config.Environment`. First sanitize inherited `Environment` and read its deduplicated `PATH`; only then add controlled `PATH=filepath.Dir(ExecutablePath)+string(os.PathListSeparator)+oldPath`, `SHELL_PICKER_ADDR=Config.CallbackAddress`, and `SHELL_PICKER_TOKEN=Config.CallbackToken`. Malicious fzf defaults, stale mixed-case Windows credentials, event values, and duplicate path/token keys must be absent, with each controlled credential emitted exactly once. Start fzf with `ContainmentForegroundTree`, `ForegroundTTY: Config.ForegroundTTY`, and `WaitDelay: time.Second`; require a valid controlling terminal on Unix before process start. Feed initial NUL records on stdin, capture only result stdout, and attach UI stderr to the terminal stream. `Config.Options` contains explicit options; production passes exactly `Options(picker,prompt)`, while real-fzf integration drives interactive behavior through the terminal harness rather than environment options. Parse `cd` success as query, key `enter`, and exactly one record; parse `cp` success as key plus one or more records. Treat exit 1 or 130 with well-formed abort output as abort; reject empty frames, non-Enter acceptance, and trailing bytes without NUL.

- [ ] **Step 6: Verify spaces, process count, options, and parsing**

Run: `go test ./internal/fzf -count=1`

Expected: PASS; actions include `wait`, fuzz/property tests reject injected controls/actions, the prompt action is terminal and colon-delimited, the installed-fzf semantic gate proves Windows separators display once and delimiter-looking prompt text stays inert, malicious inherited defaults are absent, controlled callback fields win exactly once, conservative basename validation rejects metacharacters and path-list separators while a spaced directory works, normal `Run` starts one session fzf and no version probe, fzf uses `ContainmentForegroundTree` with terminal ownership/restoration on Unix, and exact NUL frames parse.

- [ ] **Step 7: Commit fzf integration**

```bash
git add internal/fzf
git commit -m "feat(fzf): add typed actions and direct callback launch"
```

### Task 12: Implement Bounded Core Preview Before the Same-Binary Callback

**Files:**
- Create: `internal/preview/detect.go`
- Create: `internal/preview/render.go`
- Create: `internal/preview/limit.go`
- Create: `internal/preview/archive.go`
- Create: `internal/preview/detect_test.go`
- Create: `internal/preview/render_test.go`
- Create: `internal/preview/limit_test.go`
- Create: `internal/preview/archive_test.go`
- Create: `internal/callback/grammar.go`
- Create: `internal/callback/dispatch.go`
- Create: `internal/callback/grammar_test.go`
- Create: `internal/callback/dispatch_test.go`
- Create: `internal/callback/cursor_unix.go`
- Create: `internal/callback/cursor_windows.go`
- Create: `internal/callback/cursor_unix_test.go`
- Create: `internal/callback/cursor_windows_test.go`
- Create: `cmd/shell-picker/signals_unix.go`
- Create: `cmd/shell-picker/signals_windows.go`
- Modify: `cmd/shell-picker/main.go`
- Modify: `internal/app/cli.go`
- Modify: `internal/app/cli_test.go`

**Interfaces:**
- Consumes: an absolute IPC-resolved filesystem candidate, bounded output streams, the single hidden callback command, fzf environment, IPC client, and typed action renderer; virtual candidates are rejected before preview rendering.
- Produces: `preview.Category`, `preview.Limits`, `preview.Options`, `preview.Detect([]byte, fs.FileInfo) (preview.Category, error)`, `preview.Render(context.Context, protocol.ResolvedCandidate, preview.Options) error`, `callback.Parse(string) (callback.Command, error)`, `callback.Dispatch(context.Context, callback.Command, callback.Dependencies) error`, `callback.SetCursor(protocol.CursorShape)`, and hidden CLI form `shell-picker --fzf-shell COMMAND_TEXT`.

- [ ] **Step 1: Write failing core-category, absolute-path, deadline, output, and archive-bound tests**

```go
func TestCorePreviewEveryCategoryHasBoundedNativeFallback(t *testing.T) {
  cases := []struct{ fixture string; want Category }{
    {"directory", CategoryDirectory}, {"readme.md", CategoryMarkdown}, {"plain.txt", CategoryText},
    {"image.png", CategoryImage}, {"document.pdf", CategoryPDF}, {"video.mp4", CategoryVideo},
    {"audio.mp3", CategoryAudio}, {"sample.zip", CategoryZip}, {"sample.gz", CategoryGzip},
    {"sample.xz", CategoryXz}, {"sample.tar", CategoryTar}, {"sample.bz2", CategoryBzip}, {"binary.bin", CategoryBinary},
  }
  for _, tc := range cases {
    result := renderNativeFixture(t, tc.fixture)
    if result.Category != tc.want || result.Bytes == 0 || result.Bytes > 4<<20 || result.DirectChildren != 0 { t.Fatalf("%s: %+v", tc.fixture, result) }
  }
}

func TestPreviewRejectsRelativePathAndNativeLimitReturnsError(t *testing.T) {
  if err := Render(context.Background(), resolvedCandidate([]byte("relative")), defaultPreviewOptions(t)); !errors.Is(err, ErrPathNotAbsolute) { t.Fatalf("relative err=%v", err) }
  options := infiniteNativeRendererOptions(t)
  err := Render(context.Background(), resolvedCandidate(absoluteFixture(t, "plain.txt")), options)
  if !errors.Is(err, ErrOutputLimit) { t.Fatalf("err=%v", err) }
}

func TestPreviewRejectsVirtualCandidateWithoutFilesystemWork(t *testing.T) {
  candidate := protocol.ResolvedCandidate{Kind: protocol.KindVirtual}
  options := optionsThatFailOnStatReadOrToolStart(t)
  if err := Render(context.Background(), candidate, options); !errors.Is(err, ErrPathNotAbsolute) { t.Fatalf("err=%v", err) }
  options.AssertNoFilesystemWork(t)
}

func TestArchiveLimitsEntriesBytesAndDeadline(t *testing.T) {
  result := renderArchiveBomb(t, 101, 4<<20+1)
  if result.Entries != DefaultLimits.MaxArchiveEntries || result.Decompressed > DefaultLimits.MaxArchiveDecompressedBytes || result.Duration > DefaultLimits.Deadline { t.Fatalf("result=%+v", result) }
}
```

- [ ] **Step 2: Run preview tests to verify the core does not exist**

Run: `go test ./internal/preview -run 'Test(CorePreview|PreviewRejects|ArchiveLimits)' -count=1`

Expected: FAIL with `undefined: Category` and `undefined: Render`.

- [ ] **Step 3: Implement all native category fallbacks and hard resource limits**

```go
type Limits struct {
  Deadline time.Duration
  MaxOutputBytes int64
  MaxInternalInputBytes int64
  MaxInternalLines int
  MaxArchiveEntries int
  MaxArchiveDecompressedBytes int64
  MaxArtifactBytes int64
}

var DefaultLimits = Limits{Deadline: 10*time.Second, MaxOutputBytes: 4<<20, MaxInternalInputBytes: 4<<20, MaxInternalLines: 10_000, MaxArchiveEntries: 100, MaxArchiveDecompressedBytes: 4<<20, MaxArtifactBytes: 64<<20}
type Options struct { Columns, Lines int; Environment []string; Runner process.Runner; Limits Limits; Stdout, Stderr io.Writer; OnDispatch func(string, int, time.Duration) }
```

Reject `candidate.Kind == protocol.KindVirtual` and require `filepath.IsAbs(string(candidate.Path))` before stat/read/tool work. Detection reads at most 64 KiB and covers directory, markdown/text, image, PDF, video, audio, zip, gzip, xz, tar, bzip, and binary. Native fallbacks are: bounded directory names; numbered escaped text; `image.DecodeConfig` metadata; PDF/media type/size/mtime metadata plus at most 64 KiB printable PDF strings; Go zip/gzip/tar listings capped by entry/decompressed/line limits; xz/bzip magic/metadata; and binary metadata. Every filesystem category emits useful nonblank fallback output even when no optional tool exists. Exact terminal renderer bytes and legacy blank-output bugs are not asserted.

Wrap every render with `renderCtx, cancel := context.WithTimeout(ctx, options.Limits.Deadline)` and defer cancel. Put a 4 MiB `countingWriter` in front of all internal/external output. Native renderers and injected unit fakes return `ErrOutputLimit` or the deadline error normally. A callback has at most one simultaneously live converter/renderer direct child and at most three sequential direct child starts; every child is waited/reaped before a fallback child starts, and native fallback starts none. Once a Unix external renderer has started with `ContainmentInheritTree`, output/deadline cancellation calls `Child.KillTree`, which kills the complete inherited callback process group, including the callback itself; production must not execute fallback or claim final telemetry afterward. Windows closes the nested renderer Job and can return through the callback, but tests do not require a fallback after a resource kill. Bound internal reads to 4 MiB and 10,000 lines. Archive readers stop at 100 entries or 4 MiB decompressed and observe context between entries/reads. Tool argument helpers require absolute paths and place `--` before paths when that tool supports it; tools lacking `--` receive only absolute paths after rejecting a leading option-shaped basename ambiguity.

- [ ] **Step 4: Verify native preview completion before callback code is written**

Run: `go test ./internal/preview -count=1`

Expected: PASS for every category, present no optional tools, absolute-path enforcement, 10-second cancellation, 4 MiB output/internal bounds, 100-entry/4 MiB archive bounds, and zero renderer children for native fallbacks.

- [ ] **Step 5: Write exhaustive grammar rejection and fake event/load callback tests**

```go
func TestParseAcceptsOnlyStaticGrammar(t *testing.T) {
  accepted := []string{"e:mi", "e:ma", "e:es", "e:fw", "e:up", "e:sl", "e:hm", "e:en", "l:1", "l:18446744073709551615", "p"}
  rejected := []string{"", "e:q", "l:0", "l:-1", "l:01", "p x", "e:en;id", "$(id)", "sh -c id", "p\x00x"}
  for _, raw := range accepted { if _, err := Parse(raw); err != nil { t.Fatalf("rejected %q: %v", raw, err) } }
  for _, raw := range rejected { if _, err := Parse(raw); err == nil { t.Fatalf("accepted %q", raw) } }
}

func TestEventReadsOnlyFZFEnvironment(t *testing.T) {
  env := mapEnv{"FZF_KEY": "enter", "FZF_QUERY": "a b", "FZF_CURRENT_ITEM": "file\tdisplay\tYQ=="}
  deps := recordingDependencies(t, env)
  if err := Dispatch(context.Background(), mustParse("e:en"), deps); err != nil { t.Fatal(err) }
  assertRecordedEvent(t, deps, protocol.Event{Opcode: protocol.OpEnter, Key: "enter", Query: []byte("a b"), CurrentItem: []byte("file\tdisplay\tYQ==")})
}
```

- [ ] **Step 6: Run callback tests to verify parser and dispatcher are absent**

Run: `go test ./internal/callback ./internal/app -run 'Test(Parse|EventReads|FZFShell)' -count=1`

Expected: FAIL with `undefined: Parse` and the CLI usage exit code 2 for `--fzf-shell`.

- [ ] **Step 7: Implement fixed parsing, event/load fakes, and production preview dispatch**

```go
type Kind uint8
const (KindEvent Kind = iota + 1; KindLoad; KindPreview)
type Command struct { Kind Kind; Opcode protocol.Opcode; Generation uint64 }
type IPCClient interface {
  Event(context.Context, sessionipc.EventRequest) (sessionipc.EventResponse, error)
  Load(context.Context, sessionipc.LoadRequest) ([]byte, error)
  ResolvePreview(context.Context, sessionipc.PreviewRequest) (sessionipc.PreviewResponse, error)
  RecordPreview(context.Context, sessionipc.PreviewRequest) error
}
type Dependencies struct {
  Client IPCClient
  LookupEnv func(string) string
  Stdout io.Writer
  Stderr io.Writer
  Preview func(context.Context, protocol.ResolvedCandidate, io.Writer, io.Writer) error
}
```

Parse by exact string comparison for event forms, exact `p`, or canonical unsigned decimal after `l:` with range `1..math.MaxUint64`. Before IPC, require key sets: `mi=i`, `ma=a`, `es=esc`, `fw` in `ctrl-l|tab|right|l`, `up` in `ctrl-h|left|h`, `sl=/`, `hm=~`, and `en=enter`; `h` and `l` are valid because Normal-mode bindings trigger the corresponding navigation actions while Insert/Add leave them unbound. Read no callback data from arguments or substitutions. Callback owns the narrow `IPCClient` consumer interface above; production supplies `*sessionipc.Client`, while event/load unit tests inject fakes. Production `p` always sends preview phase `resolve`, receives an authoritative current-member `protocol.ResolvedCandidate`, rejects `KindVirtual`, requires its existing authenticated `Path` to be absolute before filesystem work, and then calls bounded `preview.Render` with callback stdout/stderr in `preview.Options`; the callback never decodes payload authorization or treats `drives` as a path locally. `ResolvedCandidate` intentionally has no `pathutil.Location`/`Target` field because IPC resolution has already reduced the authoritative record to the preview-safe filesystem DTO. Resolution authorizes the exact record's path, not immutable inode/file identity: candidate semantics intentionally follow symlinks, external tools consume paths, and replacement or retargeting after resolution is the documented unavoidable same-user filesystem TOCTOU boundary. Do not add no-follow rejection, metadata identity locks, or private artifact copies. Send `started` telemetry through an independent soft context of at most 250 ms from `OnDispatch`. Aggregate preview runner `start|exit` observations into sequential `child_starts` and `max_live_children`, and send them with duration/outcome in `finished` through another at-most-250-ms soft context only after normal render return. Enforce `child_starts <= 3`, `max_live_children <= 1`, and zero/zero for native fallback. Telemetry failures do not replace renderer status, no raw path enters telemetry, and a Unix callback killed with its external renderer group necessarily sends no fallback or `finished` claim.

For events, apply cursor and render only typed effects. Put `//go:build !windows` on `cursor_unix.go`, `cursor_unix_test.go`, and `signals_unix.go`; Windows counterparts carry `//go:build windows`. Unix writes cursor escapes to `/dev/tty`; Windows writes to `CONOUT$`; failure is soft and action stdout stays clean. Load writes exact octets.

- [ ] **Step 8: Wire the hidden CLI without exposing secrets or accepting extra arguments**

`app.Main` accepts `--fzf-shell` only with exactly one command text. It creates the IPC client exclusively from `SHELL_PICKER_ADDR` and `SHELL_PICKER_TOKEN`, sends errors without credentials, and returns 0 on success, 2 on grammar/key errors, and 1 on transport/render failure. Public usage does not advertise hidden mode. `p` ignores query; event inputs remain the three fzf variables. Preview child environments come from `process.SanitizeEnv` and contain no credentials/fzf defaults/event data.

`cmd/shell-picker/main.go` derives the root context with `signal.NotifyContext`. Unix preview tool specs use `ContainmentInheritTree` and do not set a new process group; fzf starts/owns the callback group, and renderer resource cancellation targets that whole inherited group. Windows uses the nested non-inheritable renderer Job defined in Task 5. fzf focus replacement owns callback/tree lifetime on both platforms.

- [ ] **Step 9: Verify callback, real core preview, containment, and literal safety**

Run: `go test ./internal/callback ./internal/app -run 'Test(Parse|Dispatch|Event|Load|Preview|FZFShell)' -count=1`

Expected: PASS; event/load fakes pass, production `p` resolves membership before rendering every native filesystem category, virtual records are rejected without stat/read/tool work, stale-by-generation claims are absent, callback grammar injection starts no child, telemetry is independently bounded to 250 ms, native/fake limits return errors, Unix external-renderer cancellation targets the inherited callback group, and Windows renderer specs request a nested Job.

- [ ] **Step 10: Commit core preview and callback dispatch**

```bash
git add internal/preview internal/callback cmd/shell-picker/main.go cmd/shell-picker/signals_unix.go cmd/shell-picker/signals_windows.go internal/app/cli.go internal/app/cli_test.go
git commit -m "feat(callback): add bounded preview dispatcher"
```

### Task 13: Orchestrate the Parent Session and Public `cd`/`cp` Commands

**Files:**
- Modify: `internal/candidate/builder.go`
- Modify: `internal/candidate/builder_test.go`
- Modify: `internal/candidate/zoxide_test.go`
- Create: `internal/app/run.go`
- Create: `internal/app/run_test.go`
- Create: `internal/app/tty_unix.go`
- Create: `internal/app/tty_windows.go`
- Modify: `internal/app/cli.go`
- Modify: `internal/app/cli_test.go`

**Interfaces:**
- Consumes: command `cd|cp`, `--cwd`, `--home`, `--output nul|nuon`, optional `--fzf`, public `--zoxide-policy cached|fresh`, public `--zoxide-timeout DURATION`, process streams, and executable path.
- Produces: one complete parent lifecycle through `app.RunPicker(context.Context, app.PickerOptions, app.Dependencies) (protocol.Outcome, error)` and public CLI output.

- [ ] **Step 1: Write lifecycle, outcome, abort, and cleanup tests with fakes**

```go
func TestRunPickerOwnsOneSessionAndOneFZF(t *testing.T) {
  deps := fakeDependencies(t, acceptedCDRecord("/work/child"))
  outcome, err := RunPicker(context.Background(), PickerOptions{Picker: protocol.PickerCD, CWD: []byte("/work"), Home: []byte("/home/u"), ZoxidePolicy: candidate.ZoxideCached}, deps)
  if err != nil { t.Fatal(err) }
  if got := string(outcome.Paths[0]); got != "/work/child" { t.Fatal(got) }
  if deps.FZFStarts() != 1 || len(deps.SessionAddresses()) != 1 { t.Fatalf("counts=%+v", deps.Counts()) }
  deps.AssertClosed(t)
}

func TestRunPickerShipsWorkingPreviewCallback(t *testing.T) {
  deps := fakeDependenciesInvokingPreview(t, textRecord("/work/readme.md"))
  if _, err := RunPicker(context.Background(), testPickerOptions(), deps); err != nil { t.Fatal(err) }
  if !strings.Contains(deps.PreviewOutput(), "title") || deps.PreviewCallbacks() != 1 { t.Fatalf("preview=%q callbacks=%d", deps.PreviewOutput(), deps.PreviewCallbacks()) }
}

func TestAbortWritesNoNULAndChangesNothing(t *testing.T) {
  deps := fakeAbortDependencies(t)
  outcome, err := RunPicker(context.Background(), PickerOptions{Picker: protocol.PickerCP, CWD: []byte("/work"), Home: []byte("/home/u"), ZoxidePolicy: candidate.ZoxideCached}, deps)
  if err != nil || outcome.Status != protocol.StatusAborted || len(outcome.Paths) != 0 { t.Fatalf("outcome=%+v err=%v", outcome, err) }
}

func TestPickerCLIZoxideTimeoutDefaultOverrideAndUnlimited(t *testing.T) {
  assertParsedZoxideTimeout(t, pickerArgs("cd"), candidate.DefaultZoxideTimeout())
  assertParsedZoxideTimeout(t, append(pickerArgs("cd"), "--zoxide-timeout", "275ms"), 275*time.Millisecond)
  assertParsedZoxideTimeout(t, append(pickerArgs("cd"), "--zoxide-timeout", "0"), 0)
  assertCLIRejects(t, append(pickerArgs("cd"), "--zoxide-timeout", "-1ms"))
  assertCLIRejects(t, append(pickerArgs("cd"), "--zoxide-timeout", "forever"))
}

func TestPickerCLIZoxidePolicyDefaultOverrideAndValidation(t *testing.T) {
  assertParsedZoxidePolicy(t, pickerArgs("cd"), candidate.ZoxideCached)
  assertParsedZoxidePolicy(t, append(pickerArgs("cd"), "--zoxide-policy", "fresh"), candidate.ZoxideFresh)
  assertCLIRejects(t, append(pickerArgs("cd"), "--zoxide-policy", "stale"))
  assertCLIRejects(t, append(pickerArgs("cd"), "--zoxide-policy", "cached", "--zoxide-policy", "fresh"))
}

func TestRunPickerAppliesZoxidePolicyProcessBudgets(t *testing.T) {
  cases := []struct{ name string; picker protocol.Picker; policy candidate.ZoxidePolicy; generations, attempts, starts, maxLive int }{
    {"cached-cd-successful-tool", protocol.PickerCD, candidate.ZoxideCached, 3, 1, 1, 1},
    {"fresh-cd-successful-tool", protocol.PickerCD, candidate.ZoxideFresh, 3, 3, 3, 1},
    {"cached-cp", protocol.PickerCP, candidate.ZoxideCached, 3, 0, 0, 0},
    {"fresh-cp", protocol.PickerCP, candidate.ZoxideFresh, 3, 0, 0, 0},
  }
  for _, tc := range cases {
    t.Run(tc.name, func(t *testing.T) {
      deps := fakeNavigatingDependencies(t, tc.picker, tc.generations)
      options := testPickerOptions(); options.Picker, options.ZoxidePolicy = tc.picker, tc.policy
      if _, err := RunPicker(context.Background(), options, deps); err != nil { t.Fatal(err) }
      if got := deps.ZoxideCounts(); got.Attempts != tc.attempts || got.Starts != tc.starts || got.MaxLive != tc.maxLive { t.Fatalf("counts=%+v case=%+v", got, tc) }
    })
  }
}

func TestRunPickerMissingZoxideAttemptsOnceWithoutSuccessfulStart(t *testing.T) {
  deps := fakeNavigatingDependenciesWithMissingZoxide(t, protocol.PickerCD, 3)
  if _, err := RunPicker(context.Background(), testPickerOptions(), deps); err != nil { t.Fatal(err) }
  if got := deps.ZoxideCounts(); got.Attempts != 1 || got.Starts != 0 || got.MaxLive != 0 { t.Fatalf("counts=%+v", got) }
}
```

- [ ] **Step 2: Run app lifecycle tests to verify orchestration is missing**

Run: `go test ./internal/app -run 'TestRunPicker|TestAbort|TestPickerCLI' -count=1`

Expected: FAIL with `undefined: RunPicker` and CLI usage for `cd` and `cp`.

- [ ] **Step 3: Implement the exact parent lifecycle**

```go
type PickerOptions struct {
  Picker protocol.Picker
  CWD []byte
  Home []byte
  Output protocol.OutputFormat
  FZFPath string
  ExecutablePath string
  ZoxidePolicy candidate.ZoxidePolicy
  ZoxideTimeout time.Duration
}
type Dependencies struct {
  CandidateBuilder candidate.Builder
  ProcessRunner process.Runner
  ZoxidePath string
  Environment []string
  ForegroundTTY *os.File
  TTYOut io.Writer
  TTYErr io.Writer
}
```

Validate absolute existing `cwd` and directory `home`, require `ZoxidePolicy` to be exactly `candidate.ZoxideCached` or `candidate.ZoxideFresh`, and reject a negative `ZoxideTimeout`. When `Dependencies.ForegroundTTY` is nil, open `/dev/tty` on Unix or `CONIN$` on Windows through the platform file, retain the `*os.File` through fzf Wait, and close only the handle opened by the app; injected handles remain caller-owned. Treat `Dependencies.CandidateBuilder` only as unused dependency configuration: before any build, copy that unconfigured value once into one local `candidate.Builder`, then call explicit cached/fresh configuration that clears dependency `Cache`/`NewCache` state and, for fresh policy, eagerly allocates the session permit. Configure it completely from `Dependencies.ProcessRunner`, `Dependencies.ZoxidePath` (default `zoxide`), sanitized `Dependencies.Environment`, and `PickerOptions.ZoxideTimeout`, then retain and pass its pointer for the complete picker session. `Builder` must not embed `sync.Once`/another lock or lazily initialize permit state, so the sanctioned pre-use copy remains `go vet` clean. Never copy the Builder after explicit policy configuration or first `Build`, and never reuse its cache or fresh-query permit across sessions.

For `candidate.ZoxideCached`, create exactly one session-scoped `candidate.ZoxideCache`, set it on the session Builder pointer, set `Policy: candidate.ZoxideCached`, and leave `NewCache` nil. Pass that same pointer to every candidate generation. The initial `cd` build overlaps the cache's only invocation attempt with local enumeration; the session records exactly one attempt, zero or one successful OS start, and at most one live process, while later `cd` navigation reads immutable cached paths, performs fresh local enumeration, and makes no later zoxide attempt. For `candidate.ZoxideFresh`, set no shared cache and install `NewCache` as a closure that creates a new `candidate.ZoxideCache` from the same runner/path/environment/timeout for each permit-holding `cd` build; set `Policy: candidate.ZoxideFresh`. Every completed fresh `cd` generation, including the initial one, invokes that factory once and overlaps exactly one generation-local invocation attempt with local enumeration. The one shared cancellation-aware permit inside that session Builder keeps at most one zoxide query live for the session even under malicious concurrent `Build` calls; a caller cancelled while waiting returns its context cause without a factory call or attempt. Separate `RunPicker` calls own separate Builder pointers and may query concurrently; no package-global mutex serializes sessions. Under either policy, `cp` never loads a cache or invokes the factory and records zero attempts/starts. Create `Actor`, pass it the same Builder pointer, build a complete initial Insert proposal with `BuildRequest.Initial=true`, and synchronously `Apply` it as generation 1. Generate one 256-bit IPC token and a separate 128-bit internal trace session ID. Listen on loopback, set line cursor, and launch exactly one fzf with `ContainmentForegroundTree`, the retained foreground terminal, initial snapshot records, explicit `CallbackAddress`/`CallbackToken`, controlled options, and sanitized inherited environment. This normal lifecycle does not call `fzf.CheckVersion` or launch a version probe; fzf >=0.74.1 remains the externally verified runtime precondition.

Implement the IPC backend directly over actor contracts: events call standalone `session.Handle`, which reads one snapshot, reduces once, resolves any Add intent before one `Apply`, and return only after synchronous transform publication; loads require active generation and return NUL records; previews call `ResolveCurrent` for exact current-snapshot full-record membership, require a non-virtual `KindFilesystem` authoritative target, and only then stat `Record.Path`; telemetry updates bounded process/trace counters. Every method promptly honors its request context and returns after cancellation, satisfying the cooperative `sessionipc.Backend` precondition without detached goroutines. Transform and session metrics include actor queue wait, callback IPC, build source durations, `ZoxideAttempts`, `ZoxideStarts`, `ZoxideMaxLive`, full transform duration, and load latency. No handler invokes `Apply` from inside the actor loop, and no unresolved Add intent crosses the backend/actor boundary.

After fzf exits, restore line cursor and validate accepted records against the latest snapshot; never trust payloads directly. Final `ValidateCD`/`ValidateCP` reject `KindVirtual` and any non-filesystem target, including a `cp` result containing virtual marks, so Drives can never be emitted as a path. Teardown order on every success/error/abort is: mark IPC closing/reject work, cancel its base and handler contexts, close the listener, perform bounded Shutdown/Close, wait for all tracked handlers, return from `Server.Close`, then `Actor.Close`, then close idle client transport. `Handle` owns a successfully created Add tree only until it invokes `Actor.Apply`; actor close cancels an actor-owned pending transition, waits for its generation goroutine to complete, and only then rolls back its `CreatedTree` and replies. Only after all closure completes encode accepted/aborted output. `Dependencies` may contain one unexported test-only `launchFZF` function field; when nil production calls `fzf.Run` directly, and no exported launcher interface or alternate production launch path is introduced. Task 13 lifecycle tests may use this private seam, while Task 19 owns real-fzf/PTY integration.

- [ ] **Step 4: Add public command parsing and output encoding**

Accept exactly:

```text
shell-picker cd --cwd PATH --home PATH [--output nul|nuon] [--fzf PATH] [--zoxide-policy cached|fresh] [--zoxide-timeout DURATION]
shell-picker cp --cwd PATH --home PATH [--output nul|nuon] [--fzf PATH] [--zoxide-policy cached|fresh] [--zoxide-timeout DURATION]
```

Default `--output` to `nul`, `--fzf` to `fzf`, executable path from `os.Executable`, `--zoxide-policy` to `cached`, and `--zoxide-timeout` to `candidate.DefaultZoxideTimeout()` (`75ms` Linux, `150ms` Windows). Parse policy with `candidate.ParseZoxidePolicy` and duration with `time.ParseDuration`; accept only `cached|fresh` and nonnegative durations. A timeout of `0` makes each selected cache query unlimited, but exact authoritative per-generation behavior requires the combination `--zoxide-policy fresh --zoxide-timeout 0`. Preserve both parsed values in the CLI config and `PickerOptions`; do not silently replace explicit zero with the default. Reject duplicate flags, missing values, relative cwd/home, invalid policies, invalid/negative durations, and extra arguments with exit 2. Picker abort is exit 0 with `protocol.StatusAborted`; expected fzf abort and empty selection are not printed as errors. Operational failure exits 1 and leaves stdout empty. Encode accepted outcomes only after listener and actor closure; NUL preserves POSIX bytes, while NUON requires valid UTF-8.

- [ ] **Step 5: Verify package tests and command subprocess behavior**

Run: `go test -race ./internal/app -count=1`

Expected: PASS; public picker includes working `p`; policy and timeout defaults/overrides reach `PickerOptions`; invalid/duplicate policies fail; each session constructs/configures one Builder and passes only its pointer after first use; cached `cd` makes exactly one zoxide attempt with zero or one successful start and no later attempt; fresh makes exactly one attempt with zero or one successful start per completed `cd` generation and max live one within that session while independent sessions remain concurrent; a cancelled permit waiter has no factory/attempt; `cp` makes no attempts/starts; successful-tool cases assert exact starts while missing-tool cases assert attempt 1/start 0; every branch starts one fzf and no version probe, uses foreground containment, and restores/closes terminal ownership correctly; transforms publish synchronously without self-deadlock; accepted output validates current identity; abort emits no NUL bytes; and endpoint/handlers close before actor shutdown.

- [ ] **Step 6: Commit parent orchestration**

```bash
git add internal/app
git commit -m "feat(app): orchestrate picker sessions"
```

### Task 14: Add Optional Preview Tool Precedence and Completion Compatibility

**Files:**
- Modify: `internal/process/process.go`
- Modify: `internal/process/process_unix.go`
- Modify: `internal/process/process_windows.go`
- Modify: `internal/process/process_unix_test.go`
- Modify: `internal/process/process_windows_test.go`
- Modify: `internal/callback/dispatch.go`
- Modify: `internal/callback/dispatch_test.go`
- Modify: `internal/app/cli.go`
- Modify: `internal/app/cli_test.go`
- Modify: `internal/preview/detect.go`
- Modify: `internal/preview/render.go`
- Modify: `internal/preview/limit.go`
- Modify: `internal/preview/detect_test.go`
- Modify: `internal/preview/render_test.go`
- Modify: `internal/preview/limit_test.go`

**Interfaces:**
- Consumes: core native preview, dimensions from `FZF_PREVIEW_COLUMNS`/`FZF_PREVIEW_LINES`, sanitized environment, direct process runner, and output streams.
- Produces: `preview.Category`, `preview.ParsedInput`, `preview.ParseCompletionInput([]byte, bool, []byte) (preview.ParsedInput, error)`, `preview.Detect([]byte, fs.FileInfo) (preview.Category, error)`, `preview.Render(context.Context, protocol.ResolvedCandidate, preview.Options) error`, and the existing `preview.Options{Columns, Lines int; Environment []string; Runner process.Runner; Limits Limits; Stdout, Stderr io.Writer; OnDispatch func(string, int, time.Duration)}` contract.

- [ ] **Step 1: Write classification and tool/fallback decision tests**

```go
func TestDetectExtendedCategories(t *testing.T) {
  cases := []struct{ name string; bytes []byte; mode fs.FileMode; want Category }{
    {"directory", nil, fs.ModeDir, CategoryDirectory},
    {"markdown", []byte("# title\n"), 0, CategoryMarkdown},
    {"utf8", []byte("plain text\n"), 0, CategoryText},
    {"png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...), 0, CategoryImage},
    {"pdf", []byte("%PDF-1.7\n"), 0, CategoryPDF},
    {"binary", []byte{0, 1, 2, 3}, 0, CategoryBinary},
  }
  for _, tc := range cases {
    if got, _ := detectBytes(tc.name, tc.bytes, tc.mode); got != tc.want { t.Fatalf("%s=%v", tc.name, got) }
  }
}

func TestTextRendererFallbackOrder(t *testing.T) {
  tools := fakeTools("glow", "bat")
  renderFixture(t, tools, "readme.md", []byte("# title"))
  tools.AssertCalled(t, "glow", "--width", "79", absoluteFixturePath(t, "readme.md"))
  tools = fakeTools()
  output := renderFixture(t, tools, "plain.txt", []byte("one\ntwo\n"))
  if output != "1 one\n2 two\n" { t.Fatalf("output=%q", output) }
}

func TestExternalRendererTimeoutAndInfiniteOutputRequestInheritedGroupKill(t *testing.T) {
  for _, helper := range []string{"hang", "infinite-output"} {
    result := renderWithFakeInheritedChild(t, helper)
    if result.KillTreeCalls != 1 || result.FallbackCalls != 0 || result.Bytes > 4<<20 { t.Fatalf("%s: %+v", helper, result) }
  }
}

func TestImageFallbackChildrenAreSequentialAndBounded(t *testing.T) {
  result := renderWithFailingTools(t, "kitten", "chafa")
  if result.ChildStarts != 2 || result.MaxLiveChildren != 1 || result.NativeFallbacks != 1 { t.Fatalf("result=%+v", result) }
}

func TestParseCompletionInputUsesSecondNBSPFieldOnlyWhenNonLiteral(t *testing.T) {
  input := []byte("fullvalue\u00a0/tmp/actual\u00a0index")
  got, err := ParseCompletionInput(input, false, []byte("/home/u"))
  if err != nil || string(got.Path) != "/tmp/actual" { t.Fatalf("got=%+v err=%v", got, err) }
  literal, err := ParseCompletionInput([]byte("/tmp/before\u00a0after"), true, []byte("/home/u"))
  if err != nil || string(literal.Path) != "/tmp/before\u00a0after" { t.Fatalf("literal=%+v err=%v", literal, err) }
}
```

- [ ] **Step 2: Run preview tests to verify detector and renderer are absent**

Run: `go test ./internal/preview -run 'TestDetectExtendedCategories|TestTextRendererFallbackOrder|TestExternalRendererTimeoutAndInfiniteOutputRequestInheritedGroupKill|TestParseCompletionInput' -count=1`

Expected: FAIL with `undefined: ParseCompletionInput` and tool precedence assertions selecting native fallback instead of the expected direct tool.

- [ ] **Step 3: Implement bounded detection and exact renderer precedence**

`ParseCompletionInput` preserves literal bytes except for the authority's leading `~/` Home expansion. Nonliteral mode splits on UTF-8 NBSP and selects field two when present, applies Home expansion, recognizes readable `path:line` and `path:line:column`, removes fzf-tab backslash escaping/one trailing completion space, and returns the highlight line; parity fixtures call it directly, while authenticated callback preview bypasses it. Detection remains bounded to 64 KiB. Optional `file` is a direct hint only and cannot override contradictory magic.

Tool precedence is exact: directory `eza` then native; markdown `glow`, bat, native; text bat then native; image kitten when terminal-qualified, chafa, native metadata; PDF/video/audio remain native until Task 15 adds bounded converters, except audio may use direct exiftool then native metadata; zip/gzip/xz/tar/bzip use their direct authority tool first when present and bounded native listing/metadata otherwise. Each direct `process.Spec` uses sanitized env, absolute path, `--` where supported, `ContainmentInheritTree`, 10-second context, 4 MiB counting writer, and a finite `WaitDelay`. On Unix that child remains in fzf's callback group; internal timeout/output cancellation kills the whole group and intentionally terminates the callback. On Windows that containment mode maps to the nested renderer Job. No command interpreter or option-shaped user path is used.

Each preview calls `OnDispatch` once after its first direct child starts or immediately before native fallback. Missing tools, spawn failures, and ordinary nonzero tool exits may continue to the next renderer or useful native fallback, but each attempted child is fully waited/reaped before the next starts. Meaningful nonblank stdout is useful even if the tool exits nonzero and suppresses later fallback; whitespace-only stdout is not useful and does not suppress fallback. The image chain starts at most kitten then chafa, so it has at most two sequential direct child starts, one simultaneously live child, and native fallback starts none. Before waiting for the first successfully started child, retain one explicit scoped `process.TreeHandle`; child `Wait` still fully reaps and joins streams while the handle remains capable of post-Wait cancellation. Once any renderer starts, `ErrWaitDelay`, a 10-second timeout, a 4 MiB overflow, or another resource cancellation during a later renderer/native fallback is terminal: invoke the retained handle, execute no further fallback, and close the handle on every path. Preview wraps a surviving-platform return in a dedicated terminal-resource error; callback dispatch recognizes it and omits `finished` telemetry. Unix normally kills the inherited callback group before return; Windows terminates the retained nested Job and returns the classified error without a finished claim. Unit fakes assert kill/close and absence of fallback/telemetry without killing the test process. Task 19's production helper-subprocess test externally proves the callback, renderer, and grandchild all exit within the bound.

- [ ] **Step 4: Verify callback remains wired to the enhanced bounded renderer**

Keep production `Dependencies.Preview` set to `preview.Render`. Verify resolve still precedes tool lookup/stat, current membership remains the only preview freshness rule, and a forged full record with a valid payload starts no tool.

- [ ] **Step 5: Verify core previews and literal unusual paths**

Run: `go test ./internal/preview ./internal/callback -run 'Test(Detect|Text|Directory|Image|Binary|Preview)' -count=1`

Expected: PASS for unusual paths, present/absent/ordinary-failing tools, exact precedence, sanitized env, absolute-path/no-option-injection arguments, at most one simultaneously live renderer child and at most two sequential image-renderer starts, zero native-fallback starts, native/fake limit errors, external limit kill requests with no fallback, inherited/nested containment, and useful fallback only where callback survival is valid.

- [ ] **Step 6: Commit optional preview precedence**

```bash
git add internal/preview
git commit -m "feat(preview): add bounded optional renderers"
```

### Task 15: Add Rich Media, Archive Listings, SHA-256 Cache, and Pruning

**Files:**
- Modify: `internal/process/process.go`
- Modify: `internal/process/process_unix.go`
- Modify: `internal/process/process_windows.go`
- Modify: `internal/process/process_unix_test.go`
- Modify: `internal/process/process_windows_test.go`
- Create: `internal/preview/cache.go`
- Create: `internal/preview/cache_posix.go`
- Create: `internal/preview/cache_quarantine_linux.go`
- Create: `internal/preview/cache_quarantine_darwin.go`
- Create: `internal/preview/cache_windows.go`
- Create: `internal/preview/cache_windows_nt.go`
- Create: `internal/preview/cache_test.go`
- Create: `internal/preview/cache_posix_test.go`
- Create: `internal/preview/cache_windows_test.go`
- Modify: `internal/preview/archive.go`
- Modify: `internal/preview/archive_test.go`
- Modify: `internal/preview/limit.go`
- Modify: `internal/preview/limit_test.go`
- Modify: `internal/preview/render.go`
- Modify: `internal/preview/render_test.go`

**Interfaces:**
- Consumes: detected rich category, authoritative absolute path bytes/stat metadata, renderer identity, cache root, direct tools, and preview dimensions.
- Produces: `preview.Cache`, `preview.NewCache(string, int64) (*Cache, error)`, `(*Cache).Key(protocol.ResolvedCandidate, string) string`, `(*Cache).Get(string) (string, bool)`, `(*Cache).Put(string, io.Reader) (string, error)`, `(*Cache).Prune() error`, and bounded archive listing functions.

- [ ] **Step 1: Write cache identity, atomicity, pruning, and archive bounds tests**

```go
func TestCacheKeyUsesPathBytesSizeMtimeAndRenderer(t *testing.T) {
  c := mustNewCache(t, t.TempDir(), 512<<20)
  base := resolved([]byte{'/', 'x', 0xff}, 10, 20)
  keys := []string{c.Key(base, "pdf-v1"), c.Key(resolved(base.Path, 11, 20), "pdf-v1"), c.Key(resolved(base.Path, 10, 21), "pdf-v1"), c.Key(base, "pdf-v2")}
  if duplicate(keys) { t.Fatalf("keys=%v", keys) }
  if len(keys[0]) != 64 { t.Fatalf("key=%q", keys[0]) }
}

func TestCachePutIsAtomicAndPrunesOldest(t *testing.T) {
  c := mustNewCache(t, t.TempDir(), 12)
  mustPut(t, c, "a", "12345678", oldTime)
  mustPut(t, c, "b", "abcdefgh", newTime)
  if _, ok := c.Get("a"); ok { t.Fatal("old cache entry survived limit") }
  if path, ok := c.Get("b"); !ok || string(mustReadFile(t, path)) != "abcdefgh" { t.Fatalf("path=%q ok=%v", path, ok) }
  assertNoTempFiles(t, c.root)
}

func TestCacheRejectsSymlinkRootEntryAndOversizedArtifact(t *testing.T) {
  realRoot := t.TempDir()
  symlinkRoot := filepath.Join(t.TempDir(), "cache")
  if err := os.Symlink(realRoot, symlinkRoot); err != nil { t.Fatal(err) }
  if _, err := NewCache(symlinkRoot, 512<<20); !errors.Is(err, ErrUnsafeCache) { t.Fatalf("root err=%v", err) }
  cache := mustNewCache(t, realRoot, 512<<20)
  if err := os.Symlink(filepath.Join(realRoot, "target"), filepath.Join(realRoot, strings.Repeat("a", 64))); err != nil { t.Fatal(err) }
  if _, ok := cache.Get(strings.Repeat("a", 64)); ok { t.Fatal("accepted symlink cache entry") }
  if _, err := cache.Put("b"+strings.Repeat("0", 63), io.LimitReader(infiniteZeroReader{}, 64<<20+1)); !errors.Is(err, ErrArtifactLimit) { t.Fatalf("artifact err=%v", err) }
}

func TestConverterPollingIsBestEffortAndFinalLstatRejectsOversize(t *testing.T) {
  result := runBurstyConverterBetweenPolls(t, 64<<20+1)
  if !errors.Is(result.Err, ErrArtifactLimit) || result.Cached || result.TempStillExists { t.Fatalf("result=%+v", result) }
  if result.PeakTransientBytes <= 64<<20 { t.Fatalf("test did not exercise poll overshoot: %+v", result) }
}

func TestCacheTwoWritersPublishSameKeyWithoutOverwriteOrLinkAttack(t *testing.T) {
  result := raceTwoWritersWithTargetAttack(t, strings.Repeat("a", 64), []byte("first"), []byte("second"))
  if result.SuccessfulPublishers != 1 || result.OverwriteObserved || result.UnsafeWinnerAccepted || result.TempFiles != 0 { t.Fatalf("result=%+v", result) }
  if !bytes.Equal(result.FinalBytes, []byte("first")) && !bytes.Equal(result.FinalBytes, []byte("second")) { t.Fatalf("final=%q", result.FinalBytes) }
}

func TestArchiveListingsAreBounded(t *testing.T) {
  for _, fixture := range []string{"sample.zip", "sample.gz", "sample.tar", "sample.tar.bz2", "sample.xz"} {
    lines := renderArchiveFixture(t, fixture, 150)
    if len(lines) > 100 { t.Fatalf("%s lines=%d", fixture, len(lines)) }
  }
}
```

- [ ] **Step 2: Run rich preview tests to verify cache and converter functions are absent**

Run: `go test ./internal/preview -run 'Test(Cache|Converter|Archive|PDF|Video|Audio)' -count=1`

Expected: FAIL with `undefined: NewCache` and `undefined: renderCachedArtifact`; bounded native archive functions already exist from Task 12.

- [ ] **Step 3: Implement exact cache hashing and atomic no-replace publication**

Hash one version byte, big-endian path length/path bytes, size, nanosecond mtime, renderer length/identity with SHA-256; use lowercase 64-character hex. Default roots are `${XDG_CACHE_HOME:-$HOME/.cache}/shell-picker/previews` and `%LOCALAPPDATA%\shell-picker\previews`; absence of both the platform cache variable and usable home is an error. `NewCache` returns `(*Cache,error)` and creates/walks every component through no-follow directory handles, rejecting symlink/reparse/non-directory components and creating missing components mode 0700. `Get` accepts only a 64-lowercase-hex key, opens it relative to a freshly validated root handle without following links, requires regular type, one link, stable inode/file ID, and at most 64 MiB, and refreshes mtime through that anchored handle. The 64 MiB value is the maximum accepted/cached artifact, not a promise about peak transient converter disk usage.

`Put` freshly opens the validated cache root as a directory handle, creates a random handle-relative same-directory temp with exclusive create mode 0600, copies through a 64 MiB acceptance writer, syncs/closes, reopens no-follow, and verifies regular type, one-link identity, and size. Cache filesystem support targets Linux, macOS, and Windows: common POSIX handle code uses `//go:build linux || darwin`; Linux quarantine uses `renameat2(RENAME_NOREPLACE)`; macOS uses exclusive `renameatx_np`; Windows files use `//go:build windows` and handle-relative NT rename. Other Unix cache builds are intentionally unsupported and may fail rather than using a racy fallback. Platform-specific implementation and test files are approved to keep every file within line limits. POSIX publication uses handle-relative `linkat` without replacement and `unlinkat` for the temp; `EEXIST` means another writer won. Windows publication uses a handle-relative no-replace file-information operation rooted at the validated cache directory; it must not depend on a separately resolved absolute `MoveFileExW` path. Never use overwrite-capable final rename. After successful publication or a loser result, accept the target only after a no-follow opened handle proves that the winner is regular, non-reparse, one-link, at most 64 MiB, and has stable inode/file ID across publication/open/acceptance; otherwise return `ErrUnsafeCache`. Every failure/loser path removes only its handle-relative temp without removing or replacing the target. `Get`, mtime refresh, staging, and `Prune` use the same anchored handle discipline; staging copies through 64 MiB plus one, syncs/closes, and validates the private destination. A barrier-controlled two-writer production `Cache.Put` race runs on supported OSes with target hardlink/symlink/root swaps and proves exactly one publication, immutable winner bytes, no unsafe object accepted, and no artifact leak. Pruning uses checked/saturating size accumulation, ignores unsafe entries, sorts oldest mtime, atomically quarantines with platform no-replace rename, validates identity, and then removes until at most 512 MiB. If neither a usable platform cache environment variable nor home exists, default root resolution returns an error rather than using the current directory.

- [ ] **Step 4: Implement direct rich renderer and archive precedence**

| Category | First choice | Second choice | Safe fallback |
|---|---|---|---|
| PDF | cached JPEG; pdftoppm args `-singlefile,-jpeg,candidate.Path,tempPrefix` | render generated JPEG via kitten/chafa | PDF metadata and first 4 KiB printable strings |
| Video | cached JPEG; ffmpegthumbnailer args `-i,candidate.Path,-o,tempJPG,-s,1080,-m` | render generated JPEG | file metadata |
| Audio | cached cover; ffmpeg args `-y,-i,candidate.Path,-an,-c:v,copy,tempJPG` | exiftool with `candidate.Path` | file metadata |
| Zip | unzip args `-l,--,candidate.Path` where supported | Go `archive/zip`, first 100 names/sizes | file metadata |
| Gzip | gzip args `-l,--,candidate.Path` | Go `compress/gzip` bounded header/size | file metadata |
| Xz | xz args `-l,--,candidate.Path` | xz magic/size metadata | file metadata |
| Tar | tar args `tf,candidate.Path`, capped in Go | Go `archive/tar`, first 100 names/sizes | file metadata |
| Bzip | tar args `tf,candidate.Path`, capped in Go | bzip magic/size metadata | file metadata |

Use renderer identities `pdf-pdftoppm-v1`, `video-ffmpegthumbnailer-v1`, and `audio-ffmpeg-cover-v1`. Every converter uses sanitized env, `ContainmentInheritTree`, the existing 10-second context, absolute arguments, and `--` where supported. Generated temp artifacts are exclusive 0600 files outside the final key. A polling guard best-effort observes size and requests whole inherited-tree/group cancellation after observing more than 64 MiB; scheduling and bursty writes may transiently exceed that size, so this is disk-pressure control rather than a hard transient cap. After normal converter exit, authoritative no-follow opened-handle validation must show a regular non-reparse file at most 64 MiB before cache insertion; reject and remove every oversized/nonregular artifact. After insertion, render only a new private bounded staging copy read through the validated immutable cache winner handle, never the original converter pathname. Keep that staged file's validated identity open and stable through renderer Wait: each sequential Unix renderer receives a freshly rewound/reopened inherited read descriptor and fd path; Windows keeps a read handle that denies write/delete/replacement sharing. This launches no additional process. On Windows terminal cleanup condition-polls anchored deletion for up to one second after Job termination. If an unrelated process still holds a conflicting lock, close all owned handles and return, leaving only the validated 0700/0600 private staging artifact; no helper process or detached goroutine is created. NewCache and Prune perform anchored identity-safe cleanup of stale private staging names. This is a bounded best-effort cleanup guarantee, not universal deletion under arbitrary external locks. Rich preview has at most one simultaneously live converter/renderer direct child; it waits/reaps each child before fallback, and converter plus kitten plus chafa is the maximum three-start sequential chain. A native fallback starts no child. Rendered stdout still passes through the 4 MiB counting writer. Optional archive tools are bounded by 100 emitted rows, 4 MiB output, and 10 seconds; the 4 MiB input/decompression bound applies to Go-managed internal reads/decompression, not private reads inside a direct external authority tool.

- [ ] **Step 5: Verify every category with present and absent tools**

Run: `go test ./internal/preview -count=1`

Expected: PASS for cache hits and two-writer same-key races, symlink/reparse/nonregular/link-attack rejection, exclusive no-replace atomic publication on Unix and Windows, no overwrite or temp leak, best-effort converter polling with possible transient overshoot, authoritative 64 MiB accepted/cached rejection, at most one simultaneously live converter/renderer and at most three sequential direct child starts with zero for native fallback, hanging/oversized converter tree cancellation without a fallback claim, corrupt/decompression-bomb archives, 100-entry/4 MiB/10-second limits, 512 MiB pruning, and useful native fallback for missing/spawn/ordinary-exit tool failures.

- [ ] **Step 6: Commit rich previews**

```bash
git add internal/preview
git commit -m "feat(preview): add rich media cache and archives"
```

### Task 16: Add the Exact Zsh Adapter and Integration Tests

**Files:**
- Create: `adapters/zsh/shell-picker.plugin.zsh`
- Create: `adapters/zsh/shell-picker.plugin.test.zsh`
- Create: `integration/zsh_adapter_test.go`

**Interfaces:**
- Consumes: `shell-picker cd|cp --output nul`, ZLE `BUFFER`, `CURSOR`, `LBUFFER`, `RBUFFER`, `magic-space`, existing `fzf_completion`, and NUL output files.
- Produces: `_shell_picker_cd`, `_shell_picker_cp`, `_shell_picker_tab`, `_shell_picker_space`, and registration through `zle -N` without rebinding Tab until the user calls `shell-picker-bind-zsh`.

- [ ] **Step 1: Write failing ZLE tests for exact triggers and output behavior**

```zsh
test_cd_space_exact() {
  BUFFER='cd' CURSOR=2
  _shell_picker_space
  assert_equal 1 "$picker_cd_calls" 'exact cd Space did not launch picker'
  BUFFER='echo cd' CURSOR=7
  _shell_picker_space
  assert_equal 'echo cd ' "$BUFFER" 'ordinary Space fallback changed buffer'
  assert_equal 1 "$picker_cd_calls" 'non-exact cd launched picker'
}

test_cp_tab_parser_and_order() {
  LBUFFER='cp ' RBUFFER=
  _shell_picker_tab
  assert_equal 'cp first\ path third\ path' "$LBUFFER" 'cp paths were not inserted in validated order'
  [[ $LBUFFER != *' ' ]] || fail 'cp insertion has trailing space'
  LBUFFER='echo x | cp ' RBUFFER='suffix'
  _shell_picker_tab
  assert_equal 1 "$fzf_completion_calls" 'RBUFFER did not fall back to existing completion'
}
```

Add tests for exact current source behavior: `magic-space` runs before the `BUFFER == "cd " && CURSOR == 3` check; accepted `cd` target ending newline is read with NUL awareness, quoted as `${(q)target}`, assigned as `BUFFER="builtin cd -- ${(q)target}"`, and accepted immediately; abort restores buffer/cursor; `cp` parser resets command words after `; & && || | |& &! &|`; only command word `cp` opens the picker; accepted records preserve visible order and duplicates; unknown/malformed output changes nothing; and no accepted insertion has a trailing space.

- [ ] **Step 2: Run adapter tests to verify the plugin is absent**

Run: `zsh adapters/zsh/shell-picker.plugin.test.zsh`

Expected: FAIL with `no such file or directory: adapters/zsh/shell-picker.plugin.zsh`.

- [ ] **Step 3: Implement NUL-safe widgets with one parent invocation**

```zsh
_shell_picker_cd() {
  emulate -L zsh
  local saved=$BUFFER saved_cursor=$CURSOR output target
  output=$(mktemp "${TMPDIR:-/tmp}/shell-picker-cd.XXXXXX") || { zle redisplay; return 0; }
  command shell-picker cd --cwd "$PWD" --home "$HOME" --output nul >| "$output"
  local status=$? count=0
  while IFS= read -r -d $'\0' target; do (( ++count )); done < "$output"
  rm -f -- "$output"
  if (( status != 0 || count != 1 )); then BUFFER=$saved CURSOR=$saved_cursor; zle redisplay; return 0; fi
  BUFFER="builtin cd -- ${(q)target}"
  zle accept-line
}
```

Implement `cp` with a temporary NUL file, consume all paths into an array, quote every path with `${(q)selected}`, and append `${(j: :)quoted}` to `LBUFFER` with no suffix. `_shell_picker_tab` uses the exact marker and `${(z)input}` parser from the authority, resets command words at the eight separators, calls `_shell_picker_cp` only when the current command starts with `cp`, and otherwise calls `zle fzf_completion`. `_shell_picker_space` calls `zle magic-space`, then launches only when `BUFFER == "cd " && CURSOR -eq 3`. Adapter invocations intentionally omit `--zoxide-policy` and `--zoxide-timeout`, so they receive the public defaults of cached policy and `75ms` Linux/`150ms` Windows. `shell-picker-bind-zsh` binds Space and Tab after user/plugin initialization; it does not overwrite other bindings at source time.

- [ ] **Step 4: Add a Go subprocess gate for syntax, one invocation, and raw bytes**

`integration/zsh_adapter_test.go` locates `zsh`, skips only when unavailable, installs a fake `shell-picker` that records NUL arguments/output, and runs each named Zsh suite. Assert one parent invocation per widget, exact `--cwd` and `--home` argument bytes, no helper scripts or fzf invocation in the adapter, and successful path cases for tab, newline, NBSP, leading dash, backslash, apostrophe, and trailing space.

- [ ] **Step 5: Verify Zsh syntax and integration**

Run: `zsh -n adapters/zsh/shell-picker.plugin.zsh && zsh adapters/zsh/shell-picker.plugin.test.zsh && go test ./integration -run TestZshAdapter -count=1`

Expected: PASS with exact trigger fallback, immediate `cd`, ordered/multiple `cp`, and no trailing space.

- [ ] **Step 6: Commit the Zsh adapter**

```bash
git add adapters/zsh integration/zsh_adapter_test.go
git commit -m "feat(zsh): add parity picker widgets"
```

### Task 17: Add the Nushell Space Adapter and Safe NUON Insertion

**Files:**
- Create: `adapters/nushell/shell-picker.nu`
- Create: `adapters/nushell/shell-picker.test.nu`
- Create: `integration/nushell_adapter_test.go`

**Interfaces:**
- Consumes: Nushell 0.113.1 Reedline `commandline`, exact cursor position, `shell-picker --output nuon`, `%cd`, current `$env.config.keybindings`, and existing Tab behavior.
- Produces: `_shell_picker_space`, `_shell_picker_pick_cd`, `_shell_picker_pick_cp`, and `shell-picker-bind-nushell`.

- [ ] **Step 1: Write failing Nushell tests for exact Space routing**

```nu
def test_space_trigger_matrix [] {
  for case in [
    {buffer: "cd", cursor: 2, picker: "cd"},
    {buffer: "cp", cursor: 2, picker: "cp"},
    {buffer: " cd", cursor: 3, picker: ""},
    {buffer: "cd x", cursor: 4, picker: ""},
    {buffer: "cp", cursor: 1, picker: ""},
  ] {
    let got = (run_space_case $case.buffer $case.cursor)
    assert equal $got.picker $case.picker
    if $case.picker == "" { assert equal $got.buffer ($case.buffer | str substring 0..<$case.cursor) + " " + ($case.buffer | str substring $case.cursor..) }
  }
}

def test_cp_nuon_insertion [] {
  let got = (insert_cp_paths ["first path" "quote\"path" "line\npath"])
  assert equal $got 'cp "first path" "quote\\\"path" "line\\npath"'
  assert not ($got | str ends-with " ")
}
```

Add tests that CI runs Nushell 0.113.1 and the adapter accepts 0.113.1 or newer; accepted `cd` calls `%cd`; abort, operational failure, or malformed output leaves cwd unchanged and preserves the intercepted Space so the commandline is `cd ` or `cp `; `cp` preserves order/multiplicity; invalid NUON, scalar NUON, missing `status`, missing/non-list `paths`, and non-string path members are caught; ordinary Space inserts one literal space; cursor-trigger routing is exact; and Tab bindings remain byte-for-byte unchanged. Windows cases use paths containing native backslashes and spaces, verify `to nuon --raw` quoting and round trips, and prove `%cd` can move across volumes through a temporary substitute drive whose unused letter is selected dynamically from `Z:` down through `D:` and differs from the starting volume.

- [ ] **Step 2: Run Nushell tests to verify the adapter is absent**

Run: `nu --no-config-file adapters/nushell/shell-picker.test.nu`

Expected: FAIL because `adapters/nushell/shell-picker.nu` cannot be sourced.

- [ ] **Step 3: Implement exact cursor checks, `%cd`, and NUON-safe commandline text**

```nu
def --env _shell_picker_pick_cd [] {
  let outcome = (^shell-picker cd --cwd $env.PWD --home $nu.home-path --output nuon | complete)
  if $outcome.exit_code == 0 {
    let parsed = (try { $outcome.stdout | from nuon } catch { return })
    let paths = (try {
      if $parsed.status != "accepted" { return }
      let values = $parsed.paths
      if ($values | describe) !~ '^list' or ($values | length) != 1 { return }
      if (($values | first | describe) != 'string') { return }
      $values
    } catch { return })
    if $paths != null {
      %cd ($paths | first)
      commandline edit --replace ""
    }
  }
}

def --env _shell_picker_pick_cp [] {
  let outcome = (^shell-picker cp --cwd $env.PWD --home $nu.home-path --output nuon | complete)
  if $outcome.exit_code == 0 {
    let parsed = (try { $outcome.stdout | from nuon } catch { return })
    let paths = (try {
      if $parsed.status != "accepted" { return }
      let values = $parsed.paths
      if ($values | describe) !~ '^list' or ($values | is-empty) { return }
      if ($values | any {|path| ($path | describe) != 'string' }) { return }
      $values
    } catch { return })
    if $paths != null {
      let quoted = ($paths | each {|path| $path | to nuon --raw } | str join " ")
      commandline edit --replace $"cp ($quoted)"
    }
  }
}
```

`_shell_picker_space` reads `commandline` and cursor. For exact `cd`/`cp` at byte cursor 2 it first inserts the intercepted literal Space, then dispatches; accepted `cd` clears the buffer after `%cd`, accepted `cp` replaces `cp ` with ordered quoted sources, and abort/failure/malformed output naturally leaves `cd ` or `cp `. Every other state inserts one space and returns. Adapter invocations intentionally omit `--zoxide-policy` and `--zoxide-timeout`, so they receive the public defaults of cached policy and `75ms` Linux/`150ms` Windows. Binding appends one named Space keybinding after removing only its older same-name entry and never edits Tab. Every `from nuon`, record field access, list operation, and member type check is inside `try`. `to nuon --raw` is the sole path encoder, and join adds no trailing space.

- [ ] **Step 4: Add a Go gate with a fake picker and isolated Nushell config**

The Go test locates `nu`, skips only when unavailable, places a fake `shell-picker` executable in a temporary `PATH`, and runs the adapter test file with `--no-config-file`. Assert one invocation, exact current directory/home arguments, `%cd` changes `PWD`, Unicode and quote/newline paths round-trip through NUON, abort/error/malformed output leaves the preinserted Space, cursor-at-2 triggers while every other cursor position inserts ordinary Space, and preexisting Tab completion records are byte-for-byte unchanged. On Windows, create a temporary directory containing spaces and read the occupied-drive bitmask with `GetLogicalDrives` or an equivalent native query. Select the first unused letter in descending `Z:`..`D:` order that is explicitly different from `filepath.VolumeName(originalPWD)`, store it as `drive`, run `subst X: TEMP_PATH` with `X:` replaced by `drive`, and immediately register cleanup using `subst X: /D` with that same value. Build every mapped test path from `drive`, run `%cd` from the original volume to a backslash-and-space path there, and verify native backslashes/spaces survive fake-picker NUON output and `to nuon --raw` `cp` insertion. No substitute-drive case contains a literal drive letter. On `windows-2025`, no available candidate, a failed mapping, a failed cleanup, or unavailable Nushell behavior is a hard failure rather than a skip.

- [ ] **Step 5: Verify Nushell syntax and integration**

Run: `nu --no-config-file adapters/nushell/shell-picker.test.nu && go test ./integration -run TestNushellAdapter -count=1` on Linux and Windows; the Windows run includes the dynamically selected unused substitute-drive cross-volume cases and their cleanup.

Expected: PASS with exact cursor-triggered Space behavior, environment-changing and Windows cross-volume `%cd`, safe ordered NUON-quoted `cp` including Windows backslashes/spaces, ordinary Space fallback, and unchanged Tab completion.

- [ ] **Step 6: Commit the Nushell adapter**

```bash
git add adapters/nushell integration/nushell_adapter_test.go
git commit -m "feat(nushell): add Space picker integration"
```

### Task 18: Execute Every Parity Matrix Row on Linux and Windows

**Files:**
- Create: `integration/parity_linux_test.go`
- Create: `integration/parity_windows_test.go`
- Create: `integration/testdata/parity/golden/operations.json`
- Create: `integration/testdata/parity/golden/slash.json`
- Create: `integration/testdata/parity/golden/modal.json`
- Create: `integration/testdata/parity/golden/create.json`
- Create: `integration/testdata/parity/golden/preview.json`
- Create: `integration/testdata/parity/golden/zsh-adapter.json`
- Create: `integration/testdata/parity/golden/windows-paths.json`
- Modify: `integration/parity_manifest_test.go`

**Interfaces:**
- Consumes: all 371 `source-assertions.json` rows, public package interfaces, checked-in goldens, and optionally the pinned authority root.
- Produces: `runParityRow(t *testing.T, parityRow)`, one registered runner for every manifest `runner`, `TestPreviewCategoryMatrix`, Linux picker-semantic differential tests, and Windows semantic substitutions.

- [ ] **Step 1: Write the failing execution-coverage guard**

```go
func TestEveryParityRowHasExecutableRunner(t *testing.T) {
  rows := loadParityMatrix(t)
  executed := map[string]bool{}
  for _, row := range rows {
    runner, ok := parityRunners[row.Runner]
    if !ok { t.Fatalf("row %s has unknown runner %q", row.ID, row.Runner) }
    t.Run(row.ID, func(t *testing.T) { runner(t, row); executed[row.ID] = true })
  }
  if len(executed) != 371 { t.Fatalf("executed=%d", len(executed)) }
}
```

- [ ] **Step 2: Run the guard to expose unregistered source checks**

Run: `go test ./integration -run TestEveryParityRowHasExecutableRunner -count=1`

Expected: FAIL listing the first `runner` not present in `parityRunners`.

- [ ] **Step 3: Register behavior runners for all 13 source suites**

Use these exact runner names: `codec-roundtrip`, `record-shape`, `candidate-kind`, `candidate-display`, `candidate-payload`, `codec-reject`, `batch-golden`, `batch-error`, `enumeration`, `merge`, `operation`, `slash`, `mode`, `create`, `preview`, `zsh-cd`, `zsh-cp`, `zsh-query-binding`, and `zsh-navigation-binding`. Runners construct temp fixtures, execute public packages/adapters, and compare semantic checks or deterministic protocol goldens. `batch-error` maps removed Perl failure to a failing writer; removed external encoder cases map to malformed/partial zoxide transactions. Zsh source assertions invoke widgets/options behavior rather than matching source substrings.

Add a separate preview category matrix for directory, markdown, text, image, PDF, video, audio, zip, gzip, xz, tar, bzip, and binary. For every category run tool-present, tool-missing, ordinary tool-failure, hanging, and over-output variants as applicable. Assert selected tool precedence and useful native fallback for missing/spawn/ordinary-exit failures; for a started external renderer timeout/overflow, assert inherited-tree/group cancellation and no fallback/final-telemetry claim. Assert at most one simultaneously live converter/renderer direct child, at most three sequential direct child starts for the converter-plus-kitten-plus-chafa maximum chain, and zero starts for native fallback. Also assert absolute path preservation, bounds/containment, and no option injection. Do not compare ANSI/image renderer bytes. Add deterministic sort fixtures for ASCII case pairs, Unicode case pairs, invalid UTF-8 bytes, and raw-byte ties.

The Linux test runs every row. Zoxide differential rows that require authoritative requery freshness run with `ZoxideFresh` and timeout `0`; separate rows prove the public default `ZoxideCached` intentionally keeps one immutable session result after exactly one invocation attempt, including missing-tool attempt 1/start 0 behavior. The Windows test runs the same platform-neutral rows and substitutes these exact expectations: POSIX `/` root becomes Drives; drive-root and UNC-share-root listings contain ordinary filesystem `.` then `KindVirtual` `..` with canonical payload `ZHJpdmVz` decoding exactly to `drives` and authoritative `KindDrives` target; selecting that virtual record by forward or Enter navigates Drives; non-root `..` remains an ordinary filesystem parent; Drives emits only drive records; Home remains a filesystem path; slash opens Drives; Add in Drives is `[A!]`; preview and final `cd`/`cp` output reject virtual records, including `cp` mark sets containing one; backslash is the native separator; cross-volume `cp` is absolute. Every root wire parses with a nonempty canonical payload, and ordinary records retain unchanged filesystem path contracts. Rows requiring arbitrary invalid POSIX bytes run only on Linux and have a paired Windows Unicode/space/control-display case, not a silent skip. Zsh rows run only where Zsh is installed; their portable semantic equivalent is also covered by core session/fzf tests on Windows.

- [ ] **Step 4: Add opt-in differential refresh against the pinned authority**

When `SHELL_PICKER_AUTHORITY_ROOT=/home/antoinegs/gits/configurations` is set, verify all five SHA-256 hashes before executing source. Run the 12 currently passing source suites separately and require their verified total of 365 assertions. Run `zshrc-add-mode-query-bindings` separately and require that it fails at assertion four after three successful assertions with the pinned indentation-sensitive diagnostic; do not edit or normalize the authoritative source. Execute all six intended query-binding rows through the new semantic runners, yielding the complete 371-row coverage without falsely claiming the legacy harness is green. Generate deterministic temp trees/fake tools and compare picker codec, records, state transitions, selection, triggers, and category/tool-precedence outcomes. Run authoritative zoxide freshness comparisons in fresh policy with unlimited timeout, equivalent to public `--zoxide-policy fresh --zoxide-timeout 0`, while retaining explicit default-cached contract cases. For previews compare category, chosen tool/fallback, path, and nonblank bounded outcome, not terminal-specific bytes. The default CI path reads checked-in fixtures only.

Document intentional fixes and semantic substitutions in fixture metadata: deterministic non-locale sort; no legacy cache basename collisions; useful missing-tool fallbacks instead of blank output; full-record authorization; the Windows drive/UNC-root virtual `..` substitution with fixed `drives` token and Drives target; transactional state replacement; safe Add traversal/rollback; strict callback/action grammar; and bounded preview/archive/cache resources. These fixes/substitutions are expected differences, not parity failures.

- [ ] **Step 5: Run all matrix and differential checks**

Run: `go test ./integration -run 'Test(Parity|EveryParity)' -count=1`

Expected: PASS with exactly 371 source row subtests plus all category/tool-present/tool-absent safety cases, deterministic comparator fixtures, and no unregistered, duplicate, or unexplained semantic difference.

Run with authority: `SHELL_PICKER_AUTHORITY_ROOT=/home/antoinegs/gits/configurations go test ./integration -run TestLinuxAuthorityDifferential -count=1`

Expected: PASS after verifying the five pinned hashes, 365 assertions from the 12 green legacy suites, the exact known indentation-only failure signature from the thirteenth suite, all six replacement semantic rows, picker-semantic goldens, preview category/tool precedence, and declared intentional safety differences.

- [ ] **Step 6: Commit the complete parity port**

```bash
git add integration/parity_manifest_test.go integration/parity_linux_test.go integration/parity_windows_test.go integration/testdata/parity
git commit -m "test(parity): execute all authoritative behaviors"
```

### Task 19: Test Real fzf 0.74.1 Callbacks and One-Process Sessions

**Files:**
- Create: `integration/fzf_real_test.go`
- Create: `integration/fzf_real_linux_test.go`
- Create: `integration/fzf_real_windows_test.go`
- Create: `integration/fzf_real_pipe_test.go`
- Create: `integration/testhelper/main.go`
- Create: `internal/integration/trace.go`
- Create: `internal/integration/trace_test.go`
- Modify: `internal/app/cli.go`
- Modify: `internal/app/run.go`
- Modify: `internal/app/cli_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: a real fzf path from `SHELL_PICKER_REAL_FZF`, the built `shell-picker`, temporary candidate trees, authenticated IPC, a continuously drained PTY/ConPTY, trace/controller barriers, and the blocking renderer helper.
- Produces: minimal `integration.Trace` and public `--trace FILE` barriers, common `terminalSession`, `TestRealFZFInteractiveModesReloadAddAccept`, `TestRealFZFInteractiveAbort`, `TestRealFZFPreviewReplacementKillsWholeTree`, `TestRealFZFResizeUpdatesPreviewDimensions`, `TestRealFZFAdversarialPromptCannotInjectAction`, and `TestRealFZFExactNULPipe`.

- [ ] **Step 1: Write the common terminal contract and interactive tests before platform implementations**

```go
func requireRealFZF(t *testing.T) string {
  t.Helper()
  path := os.Getenv("SHELL_PICKER_REAL_FZF")
  if path == "" { t.Skip("set SHELL_PICKER_REAL_FZF to opt in") }
  if err := fzf.CheckVersion(context.Background(), process.Runner{}, path); err != nil { t.Fatal(err) }
  return path
}

type barrier struct { Event string; Operation string; Generation uint64; Count int }
type terminalSession interface {
  Send([]byte) error
  Resize(columns, lines uint16) error
  WaitBarrier(context.Context, barrier) traceEvent
  PID() int
  Output() []byte
  CloseInput() error
  Wait(context.Context) error
  Close() error
}

func TestRealFZFInteractiveModesReloadAddAccept(t *testing.T) {
  fixture := newRealFZFFixture(t, requireRealFZF(t), `directory with spaces`)
  term := fixture.Start(protocol.PickerCP)
  defer term.Close()
  term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
  sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
  sendAndWait(t, term, []byte("a"), barrier{Event: "callback.event", Operation: "ma", Count: 1})
  if err := term.Send([]byte("created-dir")); err != nil { t.Fatal(err) }
  sendAndWait(t, term, keyEnter, barrier{Event: "generation.publish", Generation: 2})
  sendAndWait(t, term, keyCtrlH, barrier{Event: "generation.publish", Generation: 3})
  sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
  if err := term.Send([]byte("visible")); err != nil { t.Fatal(err) }
  if err := term.Send(keySpace); err != nil { t.Fatal(err) }
  if err := term.Send(keyEnter); err != nil { t.Fatal(err) }
  if err := term.Wait(testContext(t)); err != nil { t.Fatal(err) }
  fixture.AssertAccepted(t)
  fixture.AssertOneFZFNoInterpreterOrListener(t)
  fixture.AssertCallbacksUsedSpacedExecutableDirectory(t)
}
```

`fzf_real_test.go` also defines an abort subtest that enters Normal mode and sends `q`, then requires status aborted and no accepted bytes. Mode/navigation waits consume exact callback/generation trace events and renderer waits consume controller events. Query text and Space selection are sent in order through the terminal input stream and are not assigned impossible parent trace events because fzf handles them internally. The common fixture starts the actual public picker, supplies malicious inherited fzf defaults, and records process starts without placing callback address/token in test arguments.

- [ ] **Step 2: Run with fzf 0.74.1 to expose the missing terminal implementation**

Run: `SHELL_PICKER_REAL_FZF="$(command -v fzf)" go test ./integration -run TestRealFZF -count=1 -v`

Expected: FAIL to compile with `undefined: newTerminalSession` before the Linux/Windows implementations exist.

- [ ] **Step 3: Implement the minimal redacted trace and public barrier flag**

Create `internal/integration/trace.go` with `NewTrace(io.Writer, [16]byte)`, `Trace.Event(TraceEvent)`, the redacted session/path hash rules from Global Constraints, and only the events needed at this stage: `session.start`, `generation.publish`, `fzf.start`, `fzf.exit`, `callback.event`, `callback.load`, `preview.dispatch`, `preview.finished`, and `session.close`. `callback.event` carries its typed opcode in the bounded `outcome` field. `preview.finished` is emitted only after the parent backend accepts valid `Phase:"finished"` telemetry and carries only bounded renderer plus `ok|error` outcome; rejected or killed callbacks emit none. Wire public `--trace FILE` through `app.Main` and `RunPicker`; open mode 0600, keep the parent as sole mutex-protected writer, report one write failure then disable tracing, and never inherit the path or writer into callbacks/tools. Add secrecy and exact-event tests before the real terminal harness consumes it. Task 21 extends this same type and schema with source/performance timing fields rather than creating a second trace implementation.

- [ ] **Step 4: Implement the Linux PTY, barriers, drain, resize, and pidfd observer**

Put `//go:build linux` on `fzf_real_linux_test.go`. Open `/dev/ptmx` with `unix.Open(O_RDWR|O_NOCTTY|O_CLOEXEC)`, unlock it with `TIOCSPTLCK`, obtain the number with `TIOCGPTN`, open `/dev/pts/N`, and set dimensions with `TIOCSWINSZ`. Start the built picker on the slave with `SysProcAttr{Setsid:true, Setctty:true, Ctty:0}`; close the parent's slave copy. A goroutine continuously drains the master into a mutex-protected buffer from before the first key until EOF, preventing terminal backpressure.

Create a private trace FIFO before launch, start its reader first, pass it through public `--trace`, continuously decode JSONL, and satisfy `WaitBarrier` through a condition/channel keyed by event/generation/count. The renderer helper uses a separate loopback test-controller connection with length-prefixed `renderer-started`, `grandchild-started`, `dimensions`, and `renderer-exit` messages plus explicit release commands. No test uses `time.Sleep`; every transition waits on trace/controller I/O or process-handle readiness. Resolve the callback PID as the renderer's parent from `/proc/PID/stat`, verify its executable is the spaced-directory picker, open `unix.PidfdOpen` for callback/renderer/grandchild, and use `unix.Ppoll` to measure external tree exit without trusting parent trace telemetry. Skip with a precise prerequisite error only when `/dev/ptmx`, devpts ioctls, or pidfd are unavailable.

- [ ] **Step 5: Implement the Windows ConPTY, barriers, drain, resize, and process-handle observer**

Put `//go:build windows` on `fzf_real_windows_test.go`. Require Windows build 17763 or newer. Create input/output pipes, call `windows.CreatePseudoConsole`, launch the built picker with the ConPTY process-thread attribute and retained process handle, close child-side pipe duplicates, and continuously drain the output pipe until EOF. `Resize` calls `windows.ResizePseudoConsole`; `Close` calls `windows.ClosePseudoConsole` and closes each pipe/process/thread handle exactly once. Use a Toolhelp process snapshot to resolve and verify the renderer's callback parent, then wait for callback/renderer/grandchild disappearance with retained process handles and `windows.WaitForSingleObject`, measuring cancellation-to-exit outside the picker. Create the trace/controller channels as Windows named pipes with overlapped reads; use the same framed barrier protocol and no sleeps. Tests fail, rather than silently skip, on CI runners whose build should support ConPTY but whose APIs/handle lifecycle are broken.

Unix production process code and the Linux test package may import `golang.org/x/sys/unix`; the Windows production/test packages import `golang.org/x/sys/windows`. These are uses of the already-pinned sole `golang.org/x/sys v0.47.0` module. Do not add a PTY/ConPTY module or any other dependency.

- [ ] **Step 6: Add blocking renderer/grandchild control and replacement-budget tests**

`integration/testhelper/main.go` is a normal helper command selected by explicit subcommands. `renderer` sends its PID and `FZF_PREVIEW_COLUMNS`/`FZF_PREVIEW_LINES`, starts `grandchild`, waits until killed or explicitly released, and writes no credential values. `grandchild` sends its PID and blocks. A fake optional preview-tool executable delegates directly to this helper without a shell. The controller records active callback/tree counts and timestamps from OS process handles.

```go
func TestRealFZFPreviewReplacementKillsWholeTree(t *testing.T) {
  fixture := newBlockingPreviewFixture(t, requireRealFZF(t))
  term := fixture.Start(protocol.PickerCP)
  first := fixture.WaitRendererTree(t)
  started := time.Now()
  if err := term.Send(keyDown); err != nil { t.Fatal(err) }
  second := fixture.WaitDifferentRendererTree(t, first)
  fixture.WaitTreeExitByOSHandle(t, first, 3*time.Second)
  if elapsed := time.Since(started); elapsed > 3*time.Second { t.Fatalf("old preview tree exit=%v", elapsed) }
  fixture.AssertSteadyPreviewTrees(t, 1)
  fixture.AssertMaxPreviewTreesDuringReplacement(t, 2)
  fixture.AssertNoFinishedTelemetryForKilledCallback(t, first.CallbackPID)
  fixture.Release(t, second)
}
```

This is the production helper-subprocess proof: fzf focus replacement kills the old callback, renderer, and grandchild. The same harness forces an external renderer past its output/deadline limit and proves whole inherited-group exit on Linux. Cancellation-to-tree-exit comes from pidfd/process-handle observation; parent JSONL may record dispatch/cancel requests but must not claim the exit latency. Resizing to 101x37 and then 83x29 waits for a newly focused renderer to report the corresponding preview dimensions before acceptance.

- [ ] **Step 7: Add exact raw-pipe NUL and adversarial prompt-action tests**

`fzf_real_pipe_test.go` starts real fzf with ordinary pipes, `--read0`, `--print0`, a deterministic exact query, and explicit selection/exit options; it writes records containing spaces, tabs in non-delimiter escaped display, newline payload bytes, and a final NUL, then compares output byte-for-byte including the final NUL and absence of CR insertion. This separate raw-pipe test is mandatory on Windows because ConPTY is a terminal stream and is not the authority for exact NUL framing.

Create a cwd/prompt component containing the complete adversarial corpus `x)+execute(echo injected)+change-prompt(`, backslashes, commas, colons, and spaces. Drive mode changes, Add, navigation reload, focus preview, and acceptance through the terminal. Assert no sentinel executable/file is invoked or created, all callback opcodes remain typed/expected, one fzf remains alive for the session, and the final accepted path is the authorized full record. This tests real fzf grammar handling rather than only unit parsing.

- [ ] **Step 8: Add the real-fzf Make target**

```make
real-fzf:
	test -n "$(SHELL_PICKER_REAL_FZF)"
	go test ./integration -run TestRealFZF -count=1 -v
```

The Windows workflow invokes the equivalent `go test` command directly rather than relying on `make`. The target does not install modules or terminal helpers.

- [ ] **Step 9: Verify all real-fzf cases on installed Linux and Windows binaries**

Run: `SHELL_PICKER_REAL_FZF="$(command -v fzf)" make real-fzf`

Expected: PASS; the gate's explicit `CheckVersion` proves fzf is 0.74.1 or newer without adding a normal-session preflight, the spaced-directory callback basename resolves, mode keys/reloads/Add/accept/abort are automated, resize dimensions reach previews, exact raw-pipe NUL bytes survive, adversarial prompts inject no action, one fzf starts per session, old focus trees exit within the externally measured bound, steady/replacement budgets are one/two trees, each callback has at most one simultaneously live converter/renderer and at most three sequential direct child starts, and no interpreter/listener appears.

- [ ] **Step 10: Commit real-fzf coverage**

```bash
git add integration/fzf_real_test.go integration/fzf_real_linux_test.go integration/fzf_real_windows_test.go integration/fzf_real_pipe_test.go integration/testhelper/main.go internal/integration/trace.go internal/integration/trace_test.go internal/app/cli.go internal/app/run.go internal/app/cli_test.go Makefile
git commit -m "test(fzf): automate real terminal sessions"
```

### Task 20: Add Security, Partial-State, Cancellation, and Process-Leak Integration Gates

**Files:**
- Create: `integration/security_failure_test.go`
- Create: `integration/process_leak_unix_test.go`
- Create: `integration/process_leak_windows_test.go`
- Create: `scripts/security-gate.sh`
- Modify: `Makefile`
- Modify: `internal/process/process.go`
- Modify: `internal/process/env.go`
- Modify: `internal/process/process_unix.go`
- Modify: `internal/process/process_windows.go`
- Modify: `internal/process/handles_windows.go`
- Modify: `internal/process/job_windows.go`
- Modify: `internal/callback/dispatch.go`
- Modify: `internal/callback/dispatch_test.go`
- Modify: `internal/app/run.go`
- Modify: `internal/app/run_test.go`
- Modify: `internal/session/types.go`
- Modify: `internal/session/actor.go`
- Modify: `internal/session/events.go`
- Modify: `internal/session/actor_test.go`
- Modify: `internal/session/events_test.go`
- Modify: `internal/preview/render.go`
- Modify: `internal/preview/limit.go`
- Modify: `internal/preview/archive.go`
- Modify: `internal/preview/cache.go`
- Modify: `internal/preview/render_test.go`
- Modify: `internal/preview/limit_test.go`
- Modify: `internal/preview/archive_test.go`
- Modify: `internal/preview/cache_test.go`
- Modify: `internal/sessionipc/server.go`
- Modify: `internal/sessionipc/client.go`
- Modify: `internal/sessionipc/sessionipc_test.go`
- Modify: `internal/process/process_test.go`
- Modify: `internal/process/process_unix_test.go`
- Modify: `internal/process/process_windows_test.go`
- Modify: `internal/process/handles_windows_test.go`
- Modify: `internal/process/job_windows_test.go`
- Modify: `internal/fzf/action.go`
- Modify: `internal/fzf/process.go`
- Modify: `internal/fzf/action_test.go`
- Modify: `internal/fzf/process_test.go`

**Interfaces:**
- Consumes: built application, fake blocking local/zoxide/render tools, loopback requests, and OS process inspection.
- Produces: repeatable end-to-end failure tests with bounded goroutine and process counts.

- [ ] **Step 1: Write end-to-end adversarial tests before adding final cleanup behavior**

```go
func TestForgedPayloadCannotAuthorizePreviewOrSelection(t *testing.T) {
  session := startPickerSession(t, recordsFor("/allowed"))
  known := session.ActiveRecords()[0]
  forged := protocol.WireRecord{Kind: known.Kind, Display: "forged", Payload: known.Payload}.Bytes()
  if _, err := session.ResolvePreview(forged); !errors.Is(err, sessionpkg.ErrUnknownRecord) { t.Fatalf("preview=%v", err) }
  if _, err := session.Accept([][]byte{forged}); !errors.Is(err, sessionpkg.ErrUnknownSelection) { t.Fatalf("selection=%v", err) }
  session.AssertNoToolStarted(t)
}

func TestCancelledNavigationAndPreviewLeakNothing(t *testing.T) {
  baseline := snapshotResources(t)
  runCancelledNavigationAndPreview(t, 100)
  eventually(t, 3*time.Second, func() bool { return snapshotResources(t).Equal(baseline) })
}

func TestTokenCanaryExistsOnlyInControlledCallbackEnvironment(t *testing.T) {
  canary := "TOKEN-CANARY-7b09f1"
  capture := runCanarySession(t, canary)
  for name, data := range map[string][]byte{
    "arguments": capture.Arguments, "stderr": capture.Stderr, "logs": capture.Logs,
    "traces": capture.Traces, "preview": capture.PreviewOutput, "cache": capture.CacheBytes,
    "temporary-files": capture.TempBytes, "noncallback-environments": capture.NonCallbackEnvironments,
  } {
    if bytes.Contains(data, []byte(canary)) { t.Fatalf("token canary leaked through %s", name) }
  }
  capture.AssertControlledFZFAndCallbackEnvOnly(t, canary)
}
```

- [ ] **Step 2: Run adversarial tests and capture the first resource or rollback failure**

Run: `go test -race ./integration -run 'Test(Forged|Bearer|Oversize|Cancelled|Partial|Closed|Windows|Preview|Add|Inherited|TokenCanary|Zoxide)' -count=1`

Expected: FAIL to compile with `undefined: snapshotResources` before the platform resource observers and eventual cleanup assertions are added.

- [ ] **Step 3: Cover all required failure and replacement cases**

Add deterministic subtests for: valid base64 with unknown full record; changed kind/display; wrong/missing bearer; malformed endpoint; 64 KiB plus one; 17th handler; malformed/unknown JSON; stale load generation; preview record removed from current snapshot; endpoint after close; listener-close/handler-cancel/WaitGroup/force-close order; local ReadDir error; zoxide missing/spawn-failure/nonzero/partial/malformed/timeout-before-output/timeout-after-partial/caller-cancel; timeout `0` as an unlimited query under each policy; fresh plus timeout `0` as exact authoritative per-generation behavior; cached `cd` exactly one invocation attempt per session, zero or one successful OS start, at most one live process, and no later attempt; fresh `cd` exactly one attempt and zero or one successful start per completed generation with at most one live process per session Builder while independent sessions may query concurrently; missing/spawn-failure attempt 1/start 0; zero zoxide attempts/starts/exits/processes for `cp` under either policy; old maliciously superseded completion; candidate success followed by publication cancellation; Add query traversal plus Unix symlink and Windows junction in complete base ancestry, partial mkdir, target file, build failure, shutdown, reverse rollback, and another-process namespace replacement as the TOCTOU boundary; fzf malformed output/nonzero exit/unsafe basename/path-list-separator directory/malicious inherited defaults plus proof that normal `Run` starts no version probe; callback credential control fields overriding inherited values; prompt CR/LF/NUL/action injection; preview spawn failure/hang/infinite output/oversized converter/decompression bomb/cancelled grandchild, at most one simultaneously live converter/renderer, at most three sequential starts, and zero native-fallback starts; cache symlink/nonregular/two-writer/no-replace-publication/prune failures and temp/link attacks; transient converter poll overshoot with final `Lstat` rejection; and the token canary absent from arguments, stderr, logs, traces, preview output, cache, temp files, and every noncallback environment.

IPC security subtests additionally cover chunked request bodies at 64 KiB plus one; exact literal `RequestURI` routing with empty `RawQuery`/`RawPath`; rejection before backend invocation of query, encoded path/slash, trailing slash, duplicate slash, and alternate escaped targets; and exact one-field/one-value Authorization grammar before constant-time comparison/backend invocation, including duplicate valid, duplicate valid-plus-invalid, comma-joined, alternate scheme/case, and whitespace variants. Rogue loopback tests cover event JSON, resolving-preview JSON, JSON error, load/octet-stream, and telemetry-empty responses at the exact 64 KiB, 64 MiB, and 1 KiB limits and at limit plus one, proving limit-plus-one rejection before returned data, body closure, and no inappropriate connection reuse without retaining duplicate 64 MiB allocations. Use an authorized backend record that actually resolves to `KindVirtual`, and use otherwise-valid finished telemetry required fields while varying only child/live counters so those tests reach their intended validators.

Shutdown subtests use a backend blocked until `ctx.Done()` and prove force-close cancellation makes every method return and all tracked handler goroutines join within the caller's two-second bound. They assert the production actor backend obeys the same cooperative context contract and that no backend call is detached into an untracked goroutine. Do not require or claim reclamation of a deliberately cancellation-ignoring backend; such a backend violates the `sessionipc.Backend` precondition and Go cannot forcibly terminate it without a leak.

Session/Add subtests additionally cover an unknown nonempty current-item Enter; exact Normal Escape with only `ClearMulti:true`; pure Add reduction with cloned intent and no filesystem observation/creation; one-snapshot/one-reduce/one-create/one-Apply Handle orchestration; create/check errors with only the retained Add error effect; actual Handle-created build failure, caller/session cancellation, stale base, supersede, and close; reverse rollback after generator completion; partial-create and existing-file cleanup; preserved pre-existing parents; nil/empty `ValidateCP` base rejection; Windows cross-volume absolute fallback; and all POSIX-root, drive-root, UNC-share-root, Drives, non-root, and Home variants. These cases supplement rather than replace the existing Add traversal and actor tests.

Virtual-record security subtests use an exact authorized `KindVirtual` full record and prove that membership permits only forward/Enter navigation to its authoritative Drives target. Preview resolution, final `ValidateCD`, and final `ValidateCP` reject it; a `cp` mark set containing it is rejected as a whole; and no stat, relative-path, output, or zoxide merge code receives the `drives` token. Keep separate forged kind/display/payload cases to prove exact full-record authorization still applies before these semantic checks.

Zoxide security subtests enforce that `zoxide query --list` rows are platform-absolute filesystem paths preserving arbitrary legal bytes; one relative row makes the complete result a malformed soft failure, and `KindVirtual`/`drives` never enters this validator. Race caller cancellation against the private timeout and coordinate the caller cause to occur first; assert that exact cause wins, no partial records publish, and the process is reaped. Through one fresh session `*candidate.Builder`, hold an unlimited generation, start maliciously concurrent calls, prove max-live one, and prove a cancelled permit waiter returns promptly without `NewCache` or an attempt. Through two independent session Builder pointers, prove both queries can be live together and therefore no package-global mutex exists. Assert starts equal exits/process counts with no live remainder, and keep `cp` attempts/starts/exits/processes at zero.

Process-resource subtests explicitly exercise blocking stdin `io.ReadCloser` and blocking stdout/stderr `io.WriteCloser` implementations whose nonblocking `Close` promptly releases the blocked method. Assign the same pointer closer to all three fields and prove cancellation/`WaitDelay` registers and closes it once. Also pass a value closer containing non-comparable dynamic data and prove `ErrInvalidStream` before `attempt` or process start with no panic. For cancellation and `WaitDelay`, prove `Runner` calls each registered closer exactly once, each pump exits, `Wait` returns `ErrWaitDelay` or the higher-precedence cancellation/exit/pump/observer error, and goroutines, pumps, descriptors, handles, Jobs, and child processes return to baseline. Ordinary completion must not call `Close`; direct `*os.File` streams remain open and caller-owned. Retain descendant-retained-pipe cases. Scope every bounded-cleanup/leak assertion to the supported finite/nonblocking/cooperative-or-promptly-closable stream contract, and add an API-contract test/documentation assertion stating that pumped closers require stable pointer identity, are never structurally compared, and that an arbitrary nonclosable user `Read`/`Write` blocked forever violates the precondition and cannot be forcibly reclaimed by Go.

For partial state replacement, create every tested Add tree through actual standalone `Handle`, not a synthetic pre-attached proposal. Hold a valid generation, block the Add navigation generation while it reads that directory, prove read-only snapshot/preview membership remains responsive, then cancel/supersede. Assert `Handle` read one snapshot, called `Reduce` once, created once, and transferred one complete proposal through one `Apply`; the actor cancels first, waits for the generation completion signal, leaves the created path present while the generator can read it, rolls back only after completion, replies to the old caller, and only then starts the replacement. Repeat this exact ordering for caller cancellation, session cancellation, stale base, session close, and build failure, preserving every pre-existing parent. Separately force partial-create and existing-file failures and assert internal partial cleanup plus a complete retained Add-error proposal whose effect contains only the error prompt. Assert location, prompt, mode, generation, candidate bytes, index, and effect stay original throughout failed generation ownership cases. Then succeed and assert all fields/effect switch in one reply. Measure transform duration through synchronous reply; a normal later key cannot arrive, while an explicitly concurrent malicious request cannot publish the old proposal or race rollback.

- [ ] **Step 4: Cover Windows quoting, Drives, UNC, and Job cleanup**

Put `//go:build !windows` on both Unix integration/process test files and `//go:build windows` on Windows files. On Windows, build under `C:\test root\bin with spaces\`, assert safe basename plus spaced directory, reject a directory containing `;`, preserve one-visible-backslash `C:\`/`Drives\` prompts, remove/deduplicate inherited env with `EqualFold`, pass direct metacharacter arguments, cover drive-root and UNC-share-root virtual `..` wire/target/navigation plus Drives/non-root/cross-volume behavior, prove virtual preview/final-output rejection and ordinary filesystem contracts, reject reparse Add, and inject every low-level stage from devnull/duplicate/pipe/attribute-list/update/CreateProcess through Job assignment/resume. At every stage assert exact process/thread/Job/pipe/duplicate/attribute cleanup, 0..3 pump joining under the supported stream contract, pointer-identifiable blocking read/write closer emergency release and shared-pointer deduplication, non-identifiable value-closer rejection before attempt/start, direct-file caller ownership, shared-output serialization, `WaitDelay`, context/exit/pump/observer error precedence, owned fzf/zoxide Jobs, nested renderer Job, and child/grandchild cancellation; Windows production never calls `exec.Cmd.Start`.

On Unix, assert legal non-NUL pathname bytes; foreground fzf uses a caller-owned parent tty fd greater than and different from child fd 3 as `SysProcAttr.Ctty`, also maps that tty through `ExtraFiles` to child fd 3, remaps all standard streams, successfully reads canonical `x\n` from child fd 3, restores the prior foreground process group while preserving the exact prior OS-thread signal mask and caller-installed SIGTTOU notification/handling state, never calls process-global `signal.Ignore`/`signal.Reset`, and leaks no descriptor. Cover successful restore plus implementable ioctl/mask error paths and prove the OS thread is unlocked only after exact mask restoration. Inject FreeBSD kqueue registration/wait results to prove returned counts and `EV_ERROR`/`Data` errno are validated and never mistaken for exit, cross-compile that target when it cannot run, and prove other Unix backends reject before launch. zoxide uses owned process groups, and renderers inherit the fzf-owned callback group with group-wide cancellation. Use the Task 19 helper subprocess so a forced renderer timeout/output overflow proves callback, renderer, and grandchild exit, no fallback runs, and no final telemetry is claimed. Resource snapshots compare normalized goroutine stack signatures with multiplicity and include listeners, handler count, child PIDs, temp/cache files, pumps, Jobs/handles, descriptors, and process groups. Native Windows queries `SystemExtendedHandleInformation` and compares exact current-process handle values after warmup; a test-owned identity registry accounts individually for every opened process-identity handle and scoped native-handle deltas for the server and process/Job phases, requiring each identity to disappear before deregistration. Trace and any other current-process handles are covered by the global exact-value comparison, while aggregate `GetProcessHandleCount` remains supplemental. Directory and nonregular artifact identities use Windows file IDs opened without following reparse points. Native Windows fails rather than silently degrading if the system handle query is unavailable. All eventual assertions use channels, pidfds, process handles, or bounded polling primitives and never sleep as synchronization; stream leak guarantees use only streams satisfying the documented cooperative contract.

- [ ] **Step 5: Verify repeated race and leak passes**

Run: `./scripts/security-gate.sh -race -count=10 -p=1`

Expected: PASS ten consecutive Linux race runs with no publication/rollback race, forgery, live listener/handler, child tree, handle/pump/descriptor/temp/cache leak under the supported stream contract, unsafe base/query traversal, overwrite-capable cache publication, output-limit escape, preview live/sequential-child budget violation, stale credential, token-canary leak, cached/fresh/`cp` zoxide attempt/start/max-live violation, false start requirement for missing/spawn failure, normal-session fzf version probe, SIGTTOU disposition/mask clobber, or missing rollback. Pointer-identifiable blocking pumped closers are deduplicated by reference and emergency-closed once; non-identifiable value closers reject before attempt/start without panic; ordinary completion and direct files retain caller ownership. Injected kqueue registration/wait errors are observer failures rather than exits, FreeBSD cross-compiles when no runtime is available, and unsupported Unix rejects before launch. Windows runs the same normal tests and injected launcher-stage gates without claiming race support. Arbitrary nonclosable streams that block forever are explicitly outside this resource guarantee.

- [ ] **Step 6: Commit failure hardening**

```bash
git add integration/security_failure_test.go integration/process_leak_unix_test.go integration/process_leak_windows_test.go internal/process internal/callback internal/app/run.go internal/app/run_test.go internal/session internal/preview internal/sessionipc internal/fzf
git commit -m "test(security): gate session isolation and cleanup"
```

### Task 21: Add Probe, Redacted JSONL Trace, Stable Resource Gates, and Dedicated p95 Targets

**Files:**
- Create: `internal/integration/probe.go`
- Modify: `internal/integration/trace.go`
- Create: `internal/integration/benchmark.go`
- Create: `internal/integration/probe_test.go`
- Modify: `internal/integration/trace_test.go`
- Create: `internal/integration/benchmark_test.go`
- Create: `integration/performance_test.go`
- Create: `integration/benchmark_harness_test.go`
- Modify: `internal/app/cli.go`
- Modify: `internal/app/run.go`
- Modify: `internal/app/cli_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: dependency paths, sanitized environment, terminal/cache state, lifecycle timings, path bytes, process counters, `--trace FILE`, `probe --json`, and a prebuilt picker path supplied to an external test harness.
- Produces: `integration.Probe(context.Context, integration.ProbeOptions) integration.ProbeReport`, extensions to the existing `integration.Trace` schema, `integration.RunBenchmark(context.Context, integration.BenchmarkOptions) (integration.BenchmarkReport, error)`, public `probe`, and an external benchmark harness around a prebuilt binary.

- [ ] **Step 1: Write probe and trace secrecy tests**

```go
func TestTraceRedactsSensitiveValues(t *testing.T) {
  var out bytes.Buffer
  trace := NewTrace(&out, fixedSessionID())
  rawPath := []byte("/home/user/private project")
  if err := trace.Event(TraceEvent{Name: "generation.publish", Generation: 2, Path: rawPath, CandidateCount: 4, Duration: 12*time.Millisecond, Outcome: "ok"}); err != nil { t.Fatal(err) }
  text := out.String()
  if strings.Contains(text, string(rawPath)) || strings.Contains(text, "token") || strings.Contains(text, "query") { t.Fatalf("trace leaked: %s", text) }
  if !strings.Contains(text, `"path":"sha256:`) { t.Fatalf("trace not redacted: %s", text) }
}

func TestProbeReportsHardAndSoftDependencies(t *testing.T) {
  report := Probe(context.Background(), fakeProbeOptions(t, "fzf 0.74.1", missing("zoxide"), present("bat")))
  if !report.Ready || report.FZF.Version != "0.74.1" || report.Zoxide.Status != "optional-missing" || report.Zoxide.DefaultPolicy != "cached" || report.Zoxide.ExactParity != "--zoxide-policy fresh --zoxide-timeout 0" || report.Tools["bat"].Status != "available" { t.Fatalf("report=%+v", report) }
}
```

- [ ] **Step 2: Run observability tests to verify APIs are absent**

Run: `go test ./internal/integration -run 'Test(Trace|Probe|Benchmark)' -count=1`

Expected: FAIL with `undefined: Probe` and `undefined: RunBenchmark`; the minimal Task 19 trace tests already pass.

- [ ] **Step 3: Implement probe and exact JSONL trace schema**

`probe --json` reports schema version 1, OS/arch, application version, fzf path/version/readiness, optional zoxide, default zoxide policy `cached`, exact-parity invocation `--zoxide-policy fresh --zoxide-timeout 0`, the platform default zoxide timeout (`75ms` Linux/`150ms` Windows), each preview tool, cache root/writability, terminal input/output availability, adapter environment, and Windows Job/ConPTY support. Probe verifies the external fzf >=0.74.1 installation/runtime precondition by calling `fzf.CheckVersion`; this remains separate from normal picker `Run`, which performs no version probe and does not verify the precondition. Every probe child uses `process.SanitizeEnv(os.Environ(), nil)`, so no fzf defaults or stale credentials reach tools. fzf missing/below 0.74.1 sets `ready:false`; optional absence remains soft. Sort maps for deterministic output.

Extend the Task 19 trace without changing its redaction, session-ID generation, or parent-only writer ownership. Each JSONL line has `schema,time,session,event,generation,candidate_count,renderer,child_starts,max_live_children,outcome` plus optional redacted `path`, `zoxide_policy`, counters `zoxide_attempts,zoxide_starts,zoxide_max_live`, and timing fields `actor_queue_wait_us,callback_ipc_us,local_us,zoxide_us,zoxide_outcome,transform_us,load_us`. Add allowed events `generation.start`, `generation.discard`, `preview.cancel`, and `preview.exit` to the existing set. Trace actor enqueue/reply, callback request round-trip, local/cache-load durations, selected `cached|fresh` policy, attempt/start/max-live counters, `ok|missing|process-error|malformed|timeout|cancelled|not-run|cached` zoxide outcome, synchronous transform duration, and load latency. Preview started/finished telemetry is best effort under independent 250 ms soft contexts; when present it distinguishes at most three sequential child starts from at most one simultaneously live child and records zero/zero for native fallback. Because callback-owned previews can be killed without a final telemetry request, missing `preview.exit` is valid and cancellation-to-tree-exit latency is measured only by the external PTY/ConPTY OS-handle harness, never claimed in parent JSONL. Authenticated telemetry is converted to events when available; reject caller event names/control-bearing fields and preserve the Task 19 fail-once-then-disable behavior.

- [ ] **Step 4: Define stable gates and exact dedicated benchmark scenarios**

Stable tests use `testing.AllocsPerRun` only for pure padded codec, display/action rendering, and in-memory local/cached-zoxide merge functions with fixed fixtures and recorded limits. OS-backed `ReadDir`, stat, process, HTTP, preview, and cache allocations are reported with `b.ReportMetric`/JSON but never hard-gated. Stable process gates assert one fzf and no normal-session version probe; zero local enumeration helpers; cached `cd` exactly one zoxide invocation attempt for the complete session, zero or one successful OS start, no later attempts, matching exits/process counts, and max live at most one; fresh `cd` exactly one attempt per completed generation, zero or one successful start per generation, matching exits/process counts, and max live at most one per session Builder; a cancelled waiter behind an unlimited fresh query returns promptly without factory/attempt; independent session Builders may query concurrently; missing/spawn-failure attempt 1/start 0; zero zoxide attempts/starts/exits/processes for `cp` under either policy; one event callback per transform; one steady-state preview callback/tree; at most two preview callback/trees only during replacement until the old OS-observed tree exits within the bound; at most one simultaneously live converter/renderer direct child per callback; at most three sequential direct child starts per callback; zero child starts for native fallback; symlink workers 2..8; handlers at most 16; and cancellation returns resources to baseline for direct files and supported finite/nonblocking/cooperative-or-promptly-closable pumped streams. The gate does not claim that Go can reclaim an arbitrary nonclosable user stream blocked forever.

The external harness requires `-binary` pointing to a prebuilt picker and never invokes `go run`. It records hostname, OS/arch, CPU model/count/governor or Windows power plan, memory, filesystem, terminal, fzf/Go versions, antivirus/Defender status, and power-source state. First run 50-sample baselines for no-op child spawn, loopback HTTP, and warm 1,000-entry `ReadDir`; write `host-baseline.json`. Enforce goals only when the measurement metadata fingerprint matches that baseline and each baseline coefficient of variation is at most 15%; otherwise emit `baseline-required` without passing/failing p95.

Measure 50 warm samples for default-cached startup `local-only`, `zoxide-present`, `zoxide-missing`, zoxide spawn failure, and zoxide timeout at the configured `75ms` Linux/`150ms` Windows budget; `cached-navigation` from callback receipt through immutable-cache merge, atomic publication, and action write; `fresh-navigation` at the platform default budget; `fresh-exact-parity-navigation` with timeout `0`; and preview dispatch from callback start to native fallback/direct child start. Add candidate benchmarks for initial cached overlap with exactly 10,000 zoxide records, 75/150 ms timeout discard, repeated cached navigation proving one session attempt and no later attempts, repeated fresh navigation proving attempts equal completed `cd` generations with at most one start per generation, and `cp` under both policies proving zero. Every benchmark asserts attempts, starts, exits, max-live, total process counts, and no live remainder: cached remains one session attempt, fresh max-live is one per session Builder, and `cp` is all zero. Add missing/spawn-failure attempt 1/start 0 and independent-session concurrency coverage; every fresh invocation attempt overlaps local enumeration, while no package-global mutex serializes independent sessions. Emit policy/timeout, samples, p50/p95/p99, nearest-rank calculation, zoxide attempts/starts/exits/max-live/processes, preview child-starts/max-live, metadata, and baseline comparison. The six user-approved OS p95 values remain dedicated goals and become enforced only after baseline qualification; the navigation goal applies to the default `cached-navigation`, while fresh scenarios are comparative reports with enforced process counters. Shared CI never evaluates wall time.

- [ ] **Step 5: Wire CLI flags and Make targets**

Public picker commands accept `--trace FILE`, `--zoxide-policy cached|fresh`, and `--zoxide-timeout DURATION`; trace path is not inherited by children, while the parsed policy and nonnegative duration reach `PickerOptions.ZoxidePolicy` and `PickerOptions.ZoxideTimeout` unchanged. `probe --json` is public. Dedicated measurement is only the compiled integration harness and requires `SHELL_PICKER_DEDICATED_PERF=1` plus a prebuilt binary.

```make
performance-stable:
	go test ./integration -run TestStablePerformanceGates -count=1

performance-dedicated:
	test "$${SHELL_PICKER_DEDICATED_PERF}" = 1
	go build -trimpath -o bin/shell-picker ./cmd/shell-picker
	go test -c -o bin/shell-picker-perf.test ./integration
	./bin/shell-picker-perf.test -test.run TestDedicatedBaseline -binary ./bin/shell-picker -samples 50 -output host-baseline.json
	./bin/shell-picker-perf.test -test.run TestDedicatedTargets -binary ./bin/shell-picker -samples 50 -baseline host-baseline.json -output performance.json
```

- [ ] **Step 6: Verify secrecy, resources, and deterministic benchmark math**

Run: `go test ./internal/integration ./integration -run 'Test(Trace|Probe|Benchmark|StablePerformance)' -count=1`

Expected: PASS; traces contain policy, zoxide attempt/start/max-live, preview sequential-start/max-live, and all required timing fields without secrets or false preview-exit claims; probe uses `CheckVersion`, while normal `Run` starts no version probe; probe children receive sanitized env and report cached default/fresh+`0` exact parity; zoxide missing/spawn/timeout/cached/fresh outcomes do not invent successful starts; pure allocation gates and steady/replacement/cached-session/fresh-generation process budgets hold; OS allocation reports are informational; and external prebuilt-binary baseline/nearest-rank/metadata qualification is deterministic.

- [ ] **Step 7: Commit observability and performance gates**

```bash
git add internal/integration internal/app/cli.go internal/app/run.go internal/app/cli_test.go integration/performance_test.go integration/benchmark_harness_test.go Makefile
git commit -m "feat(integration): add probe trace and performance gates"
```

### Task 22: Add Linux/Windows CI, Race, Cross-Build, Adapter, Real-fzf, and Performance Workflows

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/real-fzf.yml`
- Create: `.github/workflows/performance.yml`
- Create: `integration/workflow_test.go`
- Create: `integration/source_limits_test.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: Go 1.26.5, fzf 0.74.1 archives, Zsh, Nushell 0.113.1, the Make targets, and GitHub-hosted/self-hosted runners.
- Produces: required stable CI, opt-in/scheduled real-fzf matrices, dedicated performance matrices, and four cross-built binaries.

- [ ] **Step 1: Write workflow and Go source-limit contract tests before YAML files exist**

```go
func TestCIWorkflowContract(t *testing.T) {
  text := readWorkflow(t, "ci.yml")
  requireAll(t, text, "actions/checkout@v5", "actions/setup-go@v6", "go-version: 1.26.5", "ubuntu-24.04", "windows-2025", "go test -race", "if: ${{ always() }}", "GOOS: linux", "GOOS: windows", "GOARCH: amd64", "GOARCH: arm64", "0.113.1", "adapters-windows", "TestNushellAdapter", "TestInstalledFZFCheckVersion", "needs.adapters-windows.result", "needs.fzf-version.result", "golang.org/x/sys v0.47.0")
  rejectAll(t, text, "continue-on-error: true", "--listen", "go get")
}

func TestGoSourceLineLimits(t *testing.T) {
  root := filepath.Clean("..")
  list := exec.Command("git", "ls-files", "-z", "--cached", "--", "cmd", "internal", "integration")
  list.Dir = root
  output, err := list.Output()
  if err != nil { t.Fatalf("list checked-in Go sources: %v", err) }

  files := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
  sort.Strings(files)
  var offenders []string
  for _, relative := range files {
    if relative == "" || !strings.HasSuffix(relative, ".go") || strings.Contains("/"+relative+"/", "/vendor/") { continue }
    source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
    if err != nil { t.Fatalf("read %s: %v", relative, err) }
    parsed, err := parser.ParseFile(token.NewFileSet(), relative, source, parser.ParseComments)
    if err != nil { t.Fatalf("parse %s: %v", relative, err) }
    if ast.IsGenerated(parsed) { continue }

    lines := bytes.Count(source, []byte("\n"))
    if len(source) > 0 && source[len(source)-1] != '\n' { lines++ }
    limit := 350
    if strings.HasSuffix(relative, "_test.go") { limit = 500 }
    if lines > limit { offenders = append(offenders, fmt.Sprintf("%s: %d lines (limit %d)", relative, lines, limit)) }
  }
  sort.Strings(offenders)
  if len(offenders) > 0 { t.Fatalf("Go source line limits exceeded:\n%s", strings.Join(offenders, "\n")) }
}
```

Add contracts that stable CI has an explicit fzf-version job whose installed-binary test calls `CheckVersion`, a required Windows Nushell adapter job, and no Windows Zsh invocation. That adapter job runs the integration test which dynamically selects an unused substitute drive different from the starting volume and treats the absence of a candidate or any `subst` failure as a `windows-2025` failure. The real-fzf workflow has `workflow_dispatch`, a weekly schedule, Linux/Windows matrix, default `0.74.1`, Linux `/dev/ptmx`/devpts/pidfd prerequisite checks, and a Windows build-number check for at least 17763; performance uses only labeled self-hosted runners, `SHELL_PICKER_DEDICATED_PERF: 1`, and exact scenario IDs `cached-navigation`, `fresh-navigation`, and `fresh-exact-parity-navigation`; stable CI never invokes dedicated wall-time benchmarks. Contract tests also parse `go list -m all` output and require the main module plus only `golang.org/x/sys v0.47.0`.

`TestGoSourceLineLimits` uses NUL-delimited `git ls-files --cached` output so only checked-in files below `cmd`, `internal`, and `integration` are candidates and ignored/untracked generated or build output is never visited. It filters non-Go files and any `vendor` path, parses comments with the standard library, and excludes files recognized by `ast.IsGenerated`. For every remaining file, count LF bytes and add one only when a nonempty file has an unterminated final line; apply 350 lines to production and 500 to `_test.go`. Collect every violation, sort by repository-relative slash path, and report the complete list in one failure rather than stopping at the first offender.

- [ ] **Step 2: Run workflow tests to verify files are absent**

Run: `go test ./integration -run 'Test(CIWorkflowContract|GoSourceLineLimits)' -count=1`

Expected: FAIL with `open ../.github/workflows/ci.yml: no such file or directory`; `TestGoSourceLineLimits` either passes or deterministically lists every pre-existing file that must be split before this task can pass.

- [ ] **Step 3: Create stable CI with supported pinned action majors**

Use the supported pinned majors `actions/checkout@v5`, `actions/setup-go@v6`, `actions/upload-artifact@v4`, and `actions/download-artifact@v4`. `ci.yml` triggers pull requests and pushes to `main` and has these jobs:

| Job | Runner/matrix | Exact gate |
|---|---|---|
| `unit` | `ubuntu-24.04`, `windows-2025` | `go test ./... -count=1` and `go vet ./...` |
| `race-linux` | `ubuntu-24.04` amd64 | `go test -race ./internal/... ./integration -count=1` |
| `adapters-linux` | `ubuntu-24.04` | install Zsh and run `cargo install nu --locked --version 0.113.1`; run both adapter suites and Go adapter integration |
| `adapters-windows` | `windows-2025` | install Nushell with `cargo install nu --locked --version 0.113.1`; run `nu --no-config-file adapters/nushell/shell-picker.test.nu` and `go test ./integration -run TestNushellAdapter -count=1`, including backslash/space, dynamically selected unused-drive `subst` cross-volume `%cd` with cleanup, NUON quoting, cursor trigger, and unchanged-Tab cases; fail if no `Z:`..`D:` candidate differs from the starting volume or mapping/cleanup fails; do not install or run Zsh |
| `fzf-version` | `ubuntu-24.04`, `windows-2025` | install/checksum fzf 0.74.1 and run `go test ./internal/fzf -run TestInstalledFZFCheckVersion -count=1` with its path set, proving the external precondition through `CheckVersion` without a picker-session preflight |
| `stable-performance` | both OSes | `go test ./integration -run TestStablePerformanceGates -count=1` |
| `cross-build` | four `GOOS/GOARCH` pairs with exact `output` fields | `go test -exec=true ./...` then `CGO_ENABLED=0 go build -trimpath -o "${{ matrix.output }}" ./cmd/shell-picker` |
| `required` | `ubuntu-24.04`, needs all jobs | `if: ${{ always() }}` and reject every dependency result other than `success` |

Cross-build matrix rows are `{goos: linux, goarch: amd64, output: bin/shell-picker_linux_amd64}`, `{goos: linux, goarch: arm64, output: bin/shell-picker_linux_arm64}`, `{goos: windows, goarch: amd64, output: bin/shell-picker_windows_amd64.exe}`, and `{goos: windows, goarch: arm64, output: bin/shell-picker_windows_arm64.exe}`. Set matrix `GOOS/GOARCH` for both `go test -exec=true ./...` and build so every `!windows`/`windows` test partition compiles. Windows receives normal tests, the installed fzf `CheckVersion` gate, and the Nushell adapter gate, but no Zsh or unverified race claim. The aggregate job uses explicit checks for `needs.unit.result`, `needs.race-linux.result`, `needs.adapters-linux.result`, `needs.adapters-windows.result`, `needs.fzf-version.result`, `needs.stable-performance.result`, and `needs.cross-build.result`, exiting 1 if any value is not `success`.

- [ ] **Step 4: Create real-fzf and dedicated performance workflows**

`real-fzf.yml` triggers `workflow_dispatch` with string input `fzf_version` default `0.74.1` and Sunday schedule `17 3 * * 0`; matrix `ubuntu-24.04` and `windows-2025`; download the requested official fzf archive, verify the release checksum, set `SHELL_PICKER_REAL_FZF`, and run `go test ./integration -run TestRealFZF -count=1 -v`. Reject a resolved version below 0.74.1. Before tests, Linux asserts `/dev/ptmx` opens, devpts supports `TIOCSPTLCK`/`TIOCGPTN`, and `pidfd_open` works for the current process; Windows queries `RtlGetVersion`/`[Environment]::OSVersion.Version.Build` and requires build at least 17763 plus successful `CreatePseudoConsole`. These are required harness prerequisites, not allowed failures.

`performance.yml` triggers only `workflow_dispatch`. Matrix entries are `[self-hosted, shell-picker-perf, linux]` and `[self-hosted, shell-picker-perf, windows]`; set `SHELL_PICKER_DEDICATED_PERF=1`, build picker/harness once, record host metadata/baseline, then run default-cached local-only/zoxide-present/zoxide-missing/zoxide-timeout startup, `cached-navigation`, `fresh-navigation` at the platform timeout, `fresh-exact-parity-navigation` with timeout `0`, and preview goals. Upload baseline and performance JSON with policy/timeout/process counts. Enforce the existing navigation p95 only for `cached-navigation` when the baseline fingerprint/stability qualifies; fresh scenarios enforce process counts and report timing. Otherwise mark the run `baseline-required` without claiming target success. No pull-request/push trigger is present.

- [ ] **Step 5: Complete Make quality targets and verify workflow contracts**

Set `check` to `fmt-check`, `go vet ./...`, `go test ./... -count=1`, and `performance-stable`. Normal `go test ./...` therefore runs `TestGoSourceLineLimits`. Set `cross-build` to the four `CGO_ENABLED=0` builds. `fmt-check` runs `test -z "$(gofmt -l cmd internal integration)"` without modifying files.

Run: `go test ./integration -run 'Test(CI|RealFZFWorkflow|PerformanceWorkflow|GoSourceLineLimits)' -count=1 && make check && make cross-build`

Expected: PASS; every checked-in non-generated Go source under `cmd`, `internal`, and `integration` is within its 350/500-line limit; workflow contracts include required Linux and Windows adapter jobs plus the external fzf `CheckVersion` gate, the required aggregate depends on `adapters-windows` and every other stable job, stable tests run without wall-clock targets, and four binaries appear under `bin/`.

- [ ] **Step 6: Commit CI workflows**

```bash
git add .github/workflows/ci.yml .github/workflows/real-fzf.yml .github/workflows/performance.yml integration/workflow_test.go integration/source_limits_test.go Makefile
git commit -m "ci: add cross-platform quality gates"
```

### Task 23: Add MIT License and Complete User, Architecture, Protocol, Preview, Performance, Parity, and Security Docs

**Files:**
- Create: `LICENSE`
- Create: `README.md`
- Create: `docs/architecture.md`
- Create: `docs/protocol.md`
- Create: `docs/adapters.md`
- Create: `docs/preview.md`
- Create: `docs/performance.md`
- Create: `docs/parity.md`
- Create: `docs/security.md`
- Create: `integration/docs_test.go`

**Interfaces:**
- Consumes: the final CLI, adapter APIs, protocol constants, source parity matrix, renderer matrix, trace schema, security boundaries, and performance targets.
- Produces: checked examples and operator documentation with no behavior outside the implemented interfaces.

- [ ] **Step 1: Write documentation contract and command-example tests**

```go
func TestDocumentationContracts(t *testing.T) {
  readme := readRepoFile(t, "README.md")
  requireAll(t, readme, "shell-picker cd", "shell-picker cp", "--zoxide-policy", "cached", "fresh", "--zoxide-timeout", "75ms", "150ms", "--zoxide-policy fresh --zoxide-timeout 0", "fzf 0.74.1", "external installation/runtime precondition", "normal picker Run does not verify", "older fzf is unsupported", "cooperative stream contract", "stable pointer identity", "nonclosable", "adapters/zsh/shell-picker.plugin.zsh", "adapters/nushell/shell-picker.nu", "MIT")
  protocolDoc := readRepoFile(t, "docs/protocol.md")
  requireAll(t, protocolDoc, "e:mi", "e:en", "l:GENERATION", "FZF_CURRENT_ITEM", "KindVirtual", "drives", "ZHJpdmVz", "nonempty canonical payload", "64 KiB", "64 MiB", "1 KiB", "127.0.0.1:0", "RequestURI", "RawQuery", "RawPath", "exactly one Authorization")
  parityDoc := readRepoFile(t, "docs/parity.md")
  requireAll(t, parityDoc, "371", "365", "codec: 50", "zshrc-cp: 43", "Windows semantic substitutions", "drive root", "UNC share root", "virtual ..", "default cached", "--zoxide-policy fresh --zoxide-timeout 0")
  adaptersDoc := readRepoFile(t, "docs/adapters.md")
  requireAll(t, adaptersDoc, "default cached", "75ms", "150ms", "dynamically selected unused drive")
  architectureDoc := readRepoFile(t, "docs/architecture.md")
  requireAll(t, architectureDoc, "pure reduction", "AddIntent", "exactly one immutable snapshot", "Reduce exactly once", "CreateDirectoryTree exactly once", "Apply exactly once", "no unresolved AddIntent", "session cache", "generation-local cache", "one *candidate.Builder", "must not be copied after first use", "cancellation-aware permit", "independent sessions may query concurrently", "no package-global mutex", "cp never queries zoxide", "authoritative target", "Drives", "PthreadSigmask", "SIGTTOU", "exact prior thread mask", "child fd 3")
  previewDoc := readRepoFile(t, "docs/preview.md")
  requireAll(t, previewDoc, "KindVirtual", "not a filesystem path", "atomic no-replace", "at most one simultaneously live", "at most three sequential", "native fallback starts none")
  performanceDoc := readRepoFile(t, "docs/performance.md")
  requireAll(t, performanceDoc, "cached-navigation", "fresh-navigation", "fresh-exact-parity-navigation", "one attempt per completed fresh generation", "zero or one successful start", "per session", "attempts", "starts", "exits", "processes", "zoxide_max_live")
  securityDoc := readRepoFile(t, "docs/security.md")
  requireAll(t, securityDoc, "pure reduction", "no filesystem inspection", "cloned AddIntent", "ownership transfers to Actor.Apply", "generation completion before rollback", "cached session", "fresh generation", "absolute filesystem paths", "relative row", "malformed soft failure", "virtual token never reaches", "caller cause wins", "no partial records", "process reaped", "cancelled waiter", "without factory or attempt", "per session", "no package-global mutex", "cp", "attempt 1/start 0", "full-record authorization", "virtual records", "navigation only", "preview", "final output", "complete absolute base ancestry", "atomic no-replace", "concurrent namespace replacement by another process", "caller-owned", "stable pointer identity", "never structurally compared", "emergency cleanup", "EV_ERROR", "Data errno", "unsupported Unix", "blocked forever", "outside the resource guarantee", "process-global signal disposition", "cooperative backend", "promptly honor", "cannot forcibly stop", "limit plus one", "connection reuse", "caller-authorized trace sink", "every ancestor and target", "elevated wrappers", "untrusted trace path", "defense in depth")
}
```

- [ ] **Step 2: Run docs tests to verify documentation is absent**

Run: `go test ./integration -run TestDocumentationContracts -count=1`

Expected: FAIL with `open README.md: no such file or directory`.

- [ ] **Step 3: Write installation and adapter documentation with checked commands**

`README.md` covers supported Linux/Windows, Zsh, Nushell 0.113.1, fzf 0.74.1 minimum, optional zoxide/preview tools, Go 1.26.5 build, release install, probe/trace, adapter binding, mode keys, safe Add, and abort. Classify fzf >=0.74.1 explicitly as an external installation/runtime precondition and include the contract text: normal picker Run does not verify the version or launch a version probe; behavior with an older fzf is unsupported; `probe`, CI, and real-fzf gates call `CheckVersion`. Document public `--zoxide-policy cached|fresh` and `--zoxide-timeout DURATION`; defaults are cached policy and `75ms` Linux/`150ms` Windows. Explain that `0` makes a query unlimited but only `--zoxide-policy fresh --zoxide-timeout 0` gives exact authoritative unlimited per-generation zoxide behavior. Document that `zoxide query --list` returns absolute filesystem paths, accepted rows preserve arbitrary legal bytes in a valid platform-absolute path, relative rows are malformed soft failure, and virtual tokens never reach this check. Document soft timeout/partial-output discard; cached `cd` exactly one invocation attempt per session, zero or one successful OS start, at most one live process, immutable cached later navigation, and no later attempts; fresh `cd` exactly one attempt and zero or one successful start per completed generation, overlapped with local enumeration and at most one live per session Builder; a cancelled permit waiter returns promptly without factory/attempt; independent sessions may query concurrently without a package-global mutex; missing/spawn-failure attempt 1/start 0; and zero attempts/starts/exits/processes for `cp`. State one parent/one fzf, synchronous transforms, short-lived callbacks, and native preview fallbacks. Include the cooperative stream contract plainly: direct `*os.File` streams are caller-owned and never closed by `Runner`; non-file streams are pumped and must be finite/nonblocking/cooperative or provide a nonblocking `Close` that promptly unblocks pending operations; an emergency closer requires stable pointer identity, shared pointer identities are closed at most once, and value/non-identifiable closers reject before attempt or launch without structural comparison; `Runner` may call an accepted `Close` only for cancellation/`WaitDelay` emergency cleanup; ordinary completion does not close it; and an arbitrary nonclosable stream blocked forever violates the API precondition and is outside the resource guarantee.

`README.md` also explains that Windows drive and UNC share roots display a virtual `..` which navigates to Drives, cannot be previewed or accepted as a filesystem output, and is absent from the Drives listing itself.

`docs/adapters.md` explains Zsh trigger/parser/quoting, Nushell 0.113.1 exact cursor Space/`%cd`/try-wrapped NUON/no trailing Space/unchanged Tab, Windows backslash/space and cross-volume behavior, uninstall, and that shipped adapter commands omit zoxide flags and therefore use the default cached policy with the `75ms` Linux/`150ms` Windows timeout default. It records that CI runs Nushell on Linux and `windows-2025`, including `%cd` through a dynamically selected unused drive from `Z:`..`D:` that differs from the starting volume, same-letter `subst` cleanup, NUON quoting, cursor trigger, and unchanged Tab; the absence of a candidate or mapping/cleanup failure fails the Windows job, while Zsh remains Linux-only. It points users who require authoritative per-navigation zoxide freshness to the public fresh+`0` invocation rather than claiming that adapter default. Real-fzf PTY/ConPTY tests automate `Esc`, `i`, `a`, navigation/reload, Add, Space, Enter, resize, accept, and abort for core picker behavior. Only physical cursor appearance in a user's terminal and integration with the outer ZLE/Reedline editor remain manual checks.

- [ ] **Step 4: Write architecture, protocol, preview, performance, parity, and security references**

`docs/architecture.md` explicitly records the human decision that reduction remains strictly pure and filesystem Add creation belongs to standalone `Handle` before its one `Actor.Apply`. It defines the exclusive `Reduction` branches, cloned `AddIntent`, exactly one immutable snapshot read, exactly one `Reduce`, exactly one `CreateDirectoryTree` for Add, exactly one `Apply`, and the prohibition on unresolved intents reaching the actor. It states that `Reduce` performs no filesystem inspection/call, rollback, actor call, goroutine, or mutation; discarding an intent creates nothing; Handle owns rollback after creation and before Apply; and ownership transfers to `Actor.Apply`, which keeps the tree on publication or waits for generation completion before rollback.

`docs/security.md` records the same pure-reduction and ownership boundary, including cloned base/query bytes, lexical-only reduction, complete base-ancestry checks only in `CreateDirectoryTree`, partial-create cleanup, error-prompt-only retained Add proposals, pre-existing-parent preservation, and actual Handle-created build-failure/caller-cancel/session-cancel/stale/supersede/close tests. It states that no filesystem state is observed or created by `Reduce`, no unresolved `AddIntent` reaches the actor, and rollback never races a generator. It also defines public `--trace` as a caller-authorized sink: the caller vouches for every ancestor and the target, elevated wrappers must never accept an untrusted trace path, and final-component no-follow/type/restrictive-DACL checks are defense in depth rather than anchored traversal.

`docs/security.md` also records the human shutdown decision: every `sessionipc.Backend` method must promptly honor its request context, the production actor backend does so, and bounded `Server.Close` marks closing, cancels base/handlers, closes the listener, performs bounded Shutdown/Close, waits for all tracked handlers, and returns without detaching backend calls. It states that a cancellation-ignoring backend violates the interface precondition and cannot be forcibly stopped by Go without a goroutine leak. `docs/protocol.md` and `docs/security.md` specify exact literal `RequestURI` routing with empty `RawQuery`/`RawPath`, exact one-field/one-value `Authorization: Bearer <token>` grammar before constant-time comparison, the exact 64 KiB JSON event/preview/error, 64 MiB load/octet-stream, and 1 KiB telemetry-empty client response constants, limit-plus-one decoding/draining, unconditional body closure, and overlimit connection non-reuse.

`docs/architecture.md` diagrams complete proposed transitions, pending reply/effect/build state, read-only service during generation, synchronous transform publication, cancel-then-wait-then-rollback/reply/replacement ordering, the default cached session cache with initial local/attempt overlap and immutable cached navigation, the fresh generation-local cache factory with local/attempt overlap on every `cd` build, attempt/start/exit observation phases, the statement that `cp` never attempts zoxide, and candidate records carrying an authoritative filesystem-or-Drives target independently from their wire payload. It explains that Windows root virtual `..` navigates its Drives target while preview/final output require a filesystem target. It covers Unix foreground-TTY parent-fd `Ctty` semantics plus the separate child fd 3 `ExtraFiles` mapping/lifetime/restoration, and the critical Go distinction that `Foreground` uses a parent fd while `Setctty` uses a child fd. It documents foreground restoration's `runtime.LockOSThread` plus `unix.PthreadSigmask` sequence: save the exact prior thread mask, block SIGTTOU, perform bounded `TIOCSPGRP`, restore the exact mask on every path, then unlock, without mutating process-global signal disposition or caller notification state. It also documents direct-file caller ownership, supported pumped-stream lifecycle/emergency closure by stable pointer identity with shared-pointer deduplication and pre-attempt rejection of non-identifiable closers, the explicit nonclosable-blocked-forever exclusion, FreeBSD kqueue as a non-reaping observer with event-count and `EV_ERROR`/`Data` validation, unsupported-Unix pre-launch rejection, complete-base-ancestry Add validation, containment modes, and ordered IPC-before-actor shutdown. `docs/protocol.md` specifies the one `KIND<TAB>ESCAPED_DISPLAY<TAB>PADDED_BASE64_PAYLOAD` grammar, closed kind set including `KindVirtual`, strict nonempty canonical payload validation, the exact virtual target bytes `drives` and canonical payload `ZHJpdmVz`, unchanged arbitrary-path contracts for filesystem kinds, padded base64/NUL framing, callback grammar, three DTO routes, one bearer token, dedicated `fzf.Config.CallbackAddress`/`CallbackToken`, exact loopback URL parser, 64 KiB/16-handler limits, caller-context event routing, independently capped 250 ms preview telemetry including distinct `child_starts`/`max_live_children`, current-membership-plus-filesystem-target preview semantics, shutdown, and absence of exposed trace/secondary authentication state.

`docs/preview.md` states that `KindVirtual` is not a filesystem path and is rejected before stat/tool work, then gives category/tool precedence, useful native fallbacks, 10-second/4 MiB/archive limits, 64 MiB maximum accepted/cached artifacts, best-effort polling rather than a hard transient disk cap, absolute/no-option-injection rules, Unix inherited-group cancellation that intentionally terminates the callback without fallback/final telemetry, Windows nested containment, at most one simultaneously live converter/renderer, at most three sequential starts for converter plus kitten plus chafa, the exact statement that native fallback starts none, SHA-256 keys, `Lstat` regular validation, exclusive temps, Unix link/Windows `MoveFileExW` atomic no-replace publication, validated race winners, and non-parity renderer bytes. `docs/performance.md` defines pure allocation gates, OS reports, cached/fresh/`cp` zoxide attempts, zero-or-one successful starts, max-live counters including `zoxide_max_live`, missing/spawn-failure attempt 1/start 0, and one-steady/two-replacement preview budgets plus sequential/live renderer counters; external PTY/ConPTY cancellation-to-tree-exit measurement; exact scenario IDs `cached-navigation`, `fresh-navigation`, and `fresh-exact-parity-navigation`; metadata/baseline qualification; trace non-claims; nearest-rank math; and six dedicated goals.

`docs/parity.md` lists exact 13 intended counts totaling 371, the verified 365 green legacy assertions, that the remaining legacy suite fails at assertion four after three successful assertions, six semantic replacements, source hashes, semantic/category scope, native both-picker ignore behavior, comparator fixtures, differential command, Windows substitutions, and intentional fixes. It explicitly distinguishes the default cached performance policy from exact authoritative zoxide freshness and states that zoxide freshness differential cases use `--zoxide-policy fresh --zoxide-timeout 0`; timeout `0` alone is not labeled parity mode. `docs/security.md` documents bearer/loopback trust, token-canary coverage across arguments/stderr/logs/traces/preview/cache/temp/noncallback environments, internal trace ID, full-record authorization, typed action/prompt grammar, inherited-env sanitization before controlled callback credentials, complete absolute base ancestry and query traversal checks, ordered rollback, concurrent namespace replacement by another process as the unavoidable TOCTOU boundary, containment/limits, atomic no-replace cache publication and validated winners, cached-session/fresh-generation/`cp` zoxide attempt/start/max-live and cancellation tests, missing/spawn-failure attempt 1/start 0, and normal `Run` having no fzf version probe. It defines the stream trust/resource boundary: direct files are caller-owned; eligible pumped closers require stable pointer identity and grant emergency-only `Close` permission; one pointer shared across stream fields is registered/closed once; value and other non-identifiable closers reject before attempt/start and are never structurally compared; ordinary completion preserves ownership; blocking closer tests return pumps/resources to baseline; and arbitrary nonclosable methods blocked forever are outside the resource guarantee. It records FreeBSD kqueue event-count and `EV_ERROR`/`Data` errno validation, observer errors never being successful exit, target cross-compilation when runtime execution is unavailable, and unsupported Unix rejection before launch. It also states that foreground restoration changes only the locked OS thread's temporary mask, restores the exact prior mask, preserves caller-installed SIGTTOU handling/notification, and never changes process-global signal disposition.

`docs/architecture.md` additionally states that each picker session owns one `*candidate.Builder`, fully configures it before use, and must not copy it after first use. All concurrent fresh calls through that pointer share one cancellation-aware permit; a cancelled waiter returns its context cause without factory or attempt, while independent sessions may query concurrently and no package-global mutex exists.

`docs/performance.md` additionally defines attempts, starts, exits, total process counts, and no-live-remainder assertions. Cached benchmarks remain at one session attempt with no later attempts; fresh attempts equal completed generations with at most one start each and max-live one per session Builder; independent sessions remain concurrent; and every `cp` counter is zero. Its initial-overlap benchmark fixture contains exactly 10,000 zoxide records.

`docs/security.md` explicitly records that the `zoxide query --list` contract returns absolute filesystem paths, accepted rows preserve arbitrary legal bytes in valid platform-absolute form, a relative row is whole-buffer malformed soft failure, and the virtual token never reaches this check. It documents caller-cause precedence over a later private timeout with no partial records and a reaped process, one cancellation-aware permit per session Builder, prompt cancelled waiters without factory or attempt, independent-session concurrency with no package-global mutex, and attempt/start/exit/process/max-live assertions.

In addition, `docs/parity.md` records the Windows drive-root and UNC-share-root virtual `..` substitution, exact `drives` token and canonical payload, authoritative Drives target/navigation, unchanged non-root/filesystem records, absence of dot records on Drives, and preview/final-output rejection while retaining the exact 371/365 counts and five source hashes. `docs/security.md` states that full-record authorization is necessary but virtual records are navigation-only: preview and final `cd`/`cp` output require authoritative filesystem targets, and a `cp` mark set containing a virtual record is rejected without treating its token as a path.

- [ ] **Step 5: Add the exact MIT license and verify examples**

Use the standard MIT License text with `Copyright (c) 2026 AntoineGS`. Run every shell command block marked `check` through the docs test in a temporary home using fake optional tools; run CLI `--help`, `version`, and `probe --json` examples against the built binary. Verify every documented path exists and every documented mode/opcode matches exported constants.

Run: `go test ./integration -run TestDocumentation -count=1`

Expected: PASS with no stale command, missing path, protocol mismatch, unredacted trace example, or parity-count mismatch.

- [ ] **Step 6: Commit docs and license**

```bash
git add LICENSE README.md docs integration/docs_test.go
git commit -m "docs: document shell picker architecture and use"
```

### Task 24: Add Reproducible Four-Platform Releases, Checksums, and Version Injection

**Files:**
- Create: `.github/workflows/release.yml`
- Create: `integration/release_workflow_test.go`
- Modify: `Makefile`
- Modify: `cmd/shell-picker/main.go`
- Modify: `internal/app/version.go`
- Modify: `internal/app/cli_test.go`

**Interfaces:**
- Consumes: tags `v*`, Go 1.26.5, four target pairs, adapters, docs, MIT license, and `main.version` linker injection.
- Produces: `shell-picker_${VERSION}_${GOOS}_${GOARCH}.tar.gz` for Linux, the same stem with `.zip` for Windows, `checksums.txt`, and a GitHub release.

- [ ] **Step 1: Write release contract and injected-version tests**

```go
func TestReleaseWorkflowContract(t *testing.T) {
  text := readWorkflow(t, "release.yml")
  requireAll(t, text, "tags:", "- 'v*'", "actions/checkout@v5", "actions/setup-go@v6", "actions/upload-artifact@v4", "actions/download-artifact@v4", "linux", "windows", "amd64", "arm64", "checksums.txt", "gh release create")
  rejectAll(t, text, "latest", "continue-on-error: true", "--force")
}

func TestInjectedVersion(t *testing.T) {
  binary := buildCommand(t, `-X main.version=v1.2.3`)
  out := runCommand(t, binary, "version")
  if out != "shell-picker v1.2.3\n" { t.Fatalf("out=%q", out) }
}
```

- [ ] **Step 2: Run release tests to verify workflow is absent**

Run: `go test ./integration ./internal/app -run 'Test(Release|InjectedVersion)' -count=1`

Expected: FAIL because `TestReleaseWorkflowContract` reports `open ../.github/workflows/release.yml: no such file or directory`; `TestInjectedVersion` separately passes with `shell-picker v1.2.3`.

- [ ] **Step 3: Build deterministic release payloads**

On a `v*` tag, use a four-entry `GOOS/GOARCH` matrix and `CGO_ENABLED=0`. Build with `-trimpath -buildvcs=true -ldflags "-s -w -X main.version=${GITHUB_REF_NAME}"`. Stage exactly the platform binary, `README.md`, `LICENSE`, all `docs/*.md`, `adapters/zsh/shell-picker.plugin.zsh`, and `adapters/nushell/shell-picker.nu`. Set archive timestamps from the tagged commit time, owner/group to zero for tar, and stable lexical entry order. Name archives exactly `shell-picker_${GITHUB_REF_NAME#v}_${GOOS}_${GOARCH}.tar.gz` or `.zip`.

- [ ] **Step 4: Publish checksums and GitHub release with supported pinned action majors**

Use supported pinned `actions/checkout@v5`, `actions/setup-go@v6`, `actions/upload-artifact@v4`, and `actions/download-artifact@v4`. A final Ubuntu job downloads all four archives, sorts filenames, writes lowercase SHA-256 lines to `checksums.txt`, verifies exactly four unique archives, runs the matching Linux amd64 version command, and invokes:

```bash
gh release create "$GITHUB_REF_NAME" dist/* checksums.txt --verify-tag --generate-notes --title "$GITHUB_REF_NAME"
```

Set `GH_TOKEN: ${{ github.token }}` and workflow permission `contents: write`. Do not overwrite an existing release, force tags, or publish from a non-tag ref.

- [ ] **Step 5: Add local release verification and run the full final gate**

Add `make release-snapshot VERSION=v0.0.0-test` to create all four archives under `dist/` with the same staging rules and checksums without publishing. Add `make release-check` to unpack each archive, assert its exact file list, verify checksums, cross-check injected version strings, and scan binaries for absolute workspace paths.

On Linux amd64 run: `go test -race ./... -count=1 && make check && make cross-build && make release-snapshot VERSION=v0.0.0-test && make release-check`

Expected: PASS; all tests and races pass, four builds and four deterministic archives exist, checksums verify, every payload contains the binary/adapters/docs/license, version is `v0.0.0-test`, and binaries contain no workspace path.

- [ ] **Step 6: Commit release automation**

```bash
git add .github/workflows/release.yml integration/release_workflow_test.go Makefile cmd/shell-picker/main.go internal/app/version.go internal/app/cli_test.go
git commit -m "ci(release): publish cross-platform archives"
```

## Final Verification

- [ ] Run `go mod tidy && git diff --exit-code -- go.mod go.sum && go list -m all`; expect the pinned module files to remain unchanged and the main module plus only `golang.org/x/sys v0.47.0`. Production Windows Job code and Windows Job/ConPTY tests may import `x/sys/windows`; Unix production process code and Linux integration tests may import `x/sys/unix`; no other module exists.
- [ ] Run `gofmt -w cmd internal integration` and verify `git diff --check` reports no whitespace errors.
- [ ] Run `go test ./integration -run TestGoSourceLineLimits -count=1`; expect every checked-in non-generated `.go` file under `cmd`, `internal`, and `integration` to pass the 350-line production or 500-line `_test.go` limit, with an unterminated final line counted and every offender reported in sorted order. Normal `go test ./...` also runs this gate.
- [ ] Run `go test -race ./internal/process -run 'Test(WaitDelayClosesBlockingPumpedStreams|WaitDelayClosesSharedPumpedCloserOnce|RejectsNonIdentifiableValueCloserBeforeAttempt|Kqueue|UnsupportedUnix|OrdinaryCompletionDoesNotClosePumpedCloser|WaitIsSingleUseAndWaitDelayBoundsInheritedPipe|ForegroundTree.*SIGTTOU|ForegroundTree.*Mask)' -count=10`; expect a pointer closer shared across stdin/stdout/stderr to receive exactly one emergency `Close`, a value closer with non-comparable dynamic data to return `ErrInvalidStream` before attempt/start without panic, pumps and descendants to exit, `Wait` to return `ErrWaitDelay` or the higher-precedence cancellation/exit/pump/observer error, direct files and ordinary-completion streams to remain caller-owned, injected kqueue event-count and `EV_ERROR`/`Data` errors to fail observation rather than report exit, unsupported Unix to reject before launch, and goroutines/descriptors/handles/processes to return to baseline under the supported stream contract. Expect foreground restoration to preserve caller-installed SIGTTOU notification/handling state and the exact prior thread mask on success and implementable error paths without any process-global `signal.Ignore`/`signal.Reset`. Do not claim bounded cleanup for arbitrary nonclosable stream methods blocked forever.
- [ ] On Linux amd64, run `go test -race ./... -count=1`; expect every package and integration suite to pass with no race report. On Windows, run normal `go test ./... -count=1` without claiming race support.
- [ ] Run `go test ./internal/candidate ./internal/app ./integration -run 'Test(.*Zoxide.*|.*SessionCache.*|.*FreshPolicy.*|CallerCancellationBeforePrivateTimeoutWinsAndReaps|FreshBuilderSerializesSessionQueriesAndCancelledWaiterDoesNotAttempt|IndependentFreshSessionBuildersMayQueryConcurrently)' -count=1`; expect CLI default/validation coverage; absolute-only zoxide rows preserving arbitrary legal bytes, relative-row whole-buffer malformed soft failure, and no virtual-token absolute check; caller cause precedence when cancellation wins the race with no partial records and a reaped process; cached `cd` exactly one zoxide invocation attempt, zero or one successful OS start, at most one live process, and no later attempt; fresh `cd` exactly one attempt and zero or one successful start per completed generation with at most one live process per session Builder; a cancelled permit waiter returns promptly without factory/attempt; independent session Builders query concurrently without a global mutex; missing/spawn-failure attempt 1/start 0; zero attempts/starts/exits/processes for `cp`; matching starts/exits/process counts and no live remainder; fresh local/attempt overlap; and fresh+`0` unlimited per-generation behavior.
- [ ] Run `go test ./internal/protocol -run 'Test(PathCodecAndDisplay|RecordRequiresExactlyTwoTabsAndNULFrames|VirtualKindUsesCanonicalNonemptyDrivesToken)' -count=1`; expect the closed kind set to accept `KindVirtual` only with the fixed raw token `drives` and canonical payload `ZHJpdmVz`, empty/noncanonical/different canonical virtual payloads to fail, and arbitrary filesystem path round trips to remain unchanged.
- [ ] Run `go test -race ./internal/session -run 'Test(NormalEscapeHasOnlyClearMultiEffect|ReduceValidAddIsPureAndDiscardNeedsNoRollback|HandleAddCreatesOnceAndAppliesOnce|HandleAddCreateErrorHasOnlyErrorEffect|HandleCreatedAdd.*(BuildFailure|CallerCancel|SessionCancel|StaleBase|Supersede|Close)|ValidateCP.*(Nil|Empty|CrossVolume)|EnterUnknownCurrent|.*Root.*)' -count=10`; expect `Reduce` to perform no filesystem work and return cloned intent bytes, `Handle` to read/reduce/create/apply once, create errors to publish only the retained Add error effect, actual Handle-created trees to remain until generator completion and then reverse-roll back for every ownership failure while preserving pre-existing parents, partial-create/existing-file cases to leak nothing, Normal Escape to contain only `ClearMulti:true`, unknown Enter to reject, nil/empty bases to reject, cross-volume `cp` to stay absolute, and every POSIX/drive/UNC/Drives/non-root/Home variant to pass without races.
- [ ] Run `go test -race ./internal/sessionipc -run 'Test(ServerRejectsNonCanonicalRouteBeforeBackend|ServerRequiresExactSingleAuthorizationBeforeBackend|ServerRejectsChunkedRequestLimitPlusOne|ClientResponseLimits|ResolvePreviewRejectsBackendVirtualKind|FinishedTelemetryBoundsUseValidRequiredFields|ServerCloseCancelsCooperativeBackendAndJoinsHandlers)' -count=10`; expect only the three literal RequestURI values with empty RawQuery/RawPath and one exact Authorization field/value to reach a backend; chunked request byte 64 KiB plus one to return 413; event/preview/error JSON at 64 KiB, load at 64 MiB, and telemetry at 1 KiB to succeed when otherwise valid while each limit-plus-one case fails before returning data, closes its body, and avoids overlimit connection reuse; an actual backend-returned `KindVirtual` preview to fail; valid finished telemetry to reach exact child/live bounds; and cancellation to release the context-blocked cooperative backend and join every handler within the bound with no detached backend call. Do not test or claim forcible reclamation of a cancellation-ignoring backend.
- [ ] On Linux, run `go test ./internal/pathutil ./internal/preview ./integration -run 'Test(CreateDirectoryTreeRejectsSymlinkInBaseAncestry|.*CacheTwoWriters.*|.*Publish.*|.*Renderer.*Bounded.*)' -count=1`; expect Unix symlink base-ancestor rejection plus the Linux cache publication and renderer-bound tests to pass.
- [ ] On `windows-2025`, separately run `go test ./internal/pathutil ./internal/preview ./integration -run 'Test(CreateDirectoryTreeRejectsJunctionInBaseAncestry|AbsoluteAncestryWindowsUNC|.*CacheTwoWriters.*|.*Publish.*|.*Renderer.*Bounded.*)' -count=1`; expect the real local junction rejection, pure ordered UNC ancestry without SMB/network access, and the Windows cache publication and renderer-bound tests to pass.
- [ ] On `windows-2025`, run `go test ./internal/candidate ./internal/session ./internal/callback ./internal/app ./integration -run 'Test(WindowsDriveAndUNCRootsUseCanonicalVirtualDrivesParent|.*Virtual.*|.*Windows.*Root.*)' -count=1`; expect every drive/UNC-root wire to parse with a nonempty canonical payload, virtual `..` to decode exactly to `drives` and target `KindDrives`, ordinary records and non-root `..` to retain filesystem targets, Drives to contain no dot records, forward/Enter to navigate Drives, preview/final `cd`/`cp` to reject virtual records, and `cp` mark sets containing one to produce no output.
- [ ] Run `go vet ./...`; expect no diagnostics.
- [ ] Run `zsh -n adapters/zsh/shell-picker.plugin.zsh && zsh adapters/zsh/shell-picker.plugin.test.zsh`; expect all Zsh assertions to pass.
- [ ] Run `nu --no-config-file adapters/nushell/shell-picker.test.nu && go test ./integration -run TestNushellAdapter -count=1`; expect all Linux Nushell assertions to pass. On `windows-2025`, run `nu --no-config-file adapters/nushell/shell-picker.test.nu && go test ./integration -run TestNushellAdapter -count=1`; expect backslash/space NUON quoting, cross-volume `%cd` through a dynamically selected unused `Z:`..`D:` drive different from the starting volume, same-letter `subst` cleanup, exact cursor trigger, and unchanged Tab cases to pass, with the absence of a candidate or any `subst` failure treated as a hard failure. Run Zsh only on Linux.
- [ ] Run `go test ./integration -run 'Test(Parity|EveryParity|StablePerformance|Workflow|Documentation|Release|TokenCanary)' -count=1 -v`; expect 371 parity rows plus default-cached/fresh-exact-parity distinctions and stable resource/workflow/docs/release/token-secrecy contracts to pass.
- [ ] Run `SHELL_PICKER_AUTHORITY_ROOT=/home/antoinegs/gits/configurations go test ./integration -run TestLinuxAuthorityDifferential -count=1`; expect all five hashes, 365 green legacy assertions, the pinned known failure signature, six semantic replacements, default-cached contract cases, and authoritative zoxide freshness cases using fresh policy with timeout `0` to pass.
- [ ] On Linux with devpts and pidfd support, run `SHELL_PICKER_REAL_FZF="$(command -v fzf)" go test ./integration -run TestRealFZF -count=1 -v`; on Windows build 17763 or newer, run the equivalent command with `SHELL_PICKER_REAL_FZF` set. Expect the gate's `CheckVersion` to prove the external >=0.74.1 precondition while normal picker `Run` starts no version probe; automated modes/reload/Add/resize/accept/abort; one fzf per session; Go-supported parent-fd `Foreground` Ctty semantics, separate child fd 3 foreground-TTY access, prior-group restoration with exact thread-mask and caller-installed SIGTTOU notification preservation, no process-global signal-disposition mutation, and no descriptor leak; spaced callbacks; adversarial prompt safety; whole old-preview-tree exit within the externally measured bound; steady/replacement budgets of one/two; renderer budgets of one simultaneously live and at most three sequential starts; no false final telemetry; no interpreter/listener; and exact raw-pipe NUL behavior.
- [ ] Run `GOOS=linux GOARCH=amd64 go test -exec=true ./... && GOOS=linux GOARCH=amd64 go build ./cmd/shell-picker`, repeat with `linux/arm64`, `windows/amd64`, and `windows/arm64`; also run `GOOS=freebsd GOARCH=amd64 go test -c ./internal/process -o /tmp/shell-picker-process-freebsd.test` when a FreeBSD runtime is unavailable. Expect every release build-tagged package/test partition and all four binaries to compile, plus the non-release FreeBSD kqueue process target; this does not add a fifth release artifact.
- [ ] On dedicated labeled hosts, run `SHELL_PICKER_DEDICATED_PERF=1 make performance-dedicated`; expect host metadata, baseline, an initial-overlap benchmark with exactly 10,000 zoxide records, default-cached startup/navigation staying at one session attempt with no later attempts, missing/spawn-failure attempt-without-start scenarios, fresh default-timeout navigation with attempts equal completed generations and at most one start each, fresh+`0` exact-parity navigation, `cp` zero counters, preview sequential-start/max-live counters, zoxide attempt/start/exit/per-session-max-live/process counters with no live remainder, and goal reports. Claim p95 success only when the matching baseline qualifies.
- [ ] Run `git status --short`; expect only intentional implementation files before choosing a branch integration strategy.
