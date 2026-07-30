# shell-picker

`shell-picker` is a Go picker backend for Zsh and Nushell on Linux and Windows. It supplies directory navigation (`cd`) and safe path insertion (`cp`) through fzf, with local candidates, optional zoxide candidates, and native preview fallbacks.

## Install and verify

Build with Go 1.26.5 (the module declares the `go1.26.5` toolchain), or install a release binary for the host OS and architecture. fzf 0.74.1 or newer is an **external installation/runtime precondition**. The normal picker Run does not verify the version or launch a version probe; behavior with an older fzf is unsupported. `probe`, CI, and real-fzf gates call `CheckVersion`.

```sh check
shell-picker version
```

```sh check
shell-picker probe --json
```

`probe --json` reports redacted paths, fzf readiness, optional zoxide and preview-tool availability, cache status, terminal status, adapter availability, and Windows Job/ConPTY capability. It is the appropriate diagnostic before filing an installation issue.

`shell-picker --help` currently follows the command parser's usage-error path and prints the picker usage. The supported entry points are `version`, `probe --json`, `shell-picker cd`, and `shell-picker cp`.

## Use directly

Both picker commands require absolute `--cwd` and `--home`; output is `nul` by default or `nuon` for Nushell.

```sh check
shell-picker cd --cwd "$PWD" --home "$HOME" --output nul
shell-picker cp --cwd "$PWD" --home "$HOME" --output nul
```

The picker uses a two-line layout: the escaped current location is the header, while the prompt contains only the mode and query. Long headers are left-truncated to fit.

```text
··/long/path/to/current/
[I] query                         7/42
```

For `cp`, the info text appends ` (N)` only while a selection is active. `cd` never shows a selection count.

### Picker keys

The table is user-facing fzf behavior, not callback opcode names. In `cd`, Space clears any mark and toggles the single selection; in `cp`, Space toggles marks. `cd` sorts and has one selectable item; `cp` preserves source order and supports multiple items. Internal callback opcodes are specified only in [the protocol reference](docs/protocol.md).

| Mode | cd active fzf bindings | cp active fzf bindings |
| --- | --- | --- |
| Insert | `enter:transform(e:en)`<br>`esc:transform(e:es)`<br>`ctrl-l,tab,right:transform(e:fw)`<br>`ctrl-h,left:transform(e:up)`<br>`/:transform(e:sl)`<br>`~:transform(e:hm)` | `enter:transform(e:en)`<br>`esc:transform(e:es)`<br>`ctrl-l,tab,right:transform(e:fw)`<br>`ctrl-h,left:transform(e:up)`<br>`/:transform(e:sl)`<br>`~:transform(e:hm)` |
| Normal | `enter:transform(e:en)`<br>`esc:transform(e:es)`<br>`i:transform(e:mi)`<br>`a:transform(e:ma)`<br>`ctrl-l,tab,right:transform(e:fw)`<br>`ctrl-h,left:transform(e:up)`<br>`/:transform(e:sl)`<br>`~:transform(e:hm)`<br>`j:down`<br>`k:up`<br>`h:trigger(ctrl-h)`<br>`l:trigger(tab)`<br>`q:abort`<br>`space:clear-multi+toggle` | `enter:transform(e:en)`<br>`esc:transform(e:es)`<br>`i:transform(e:mi)`<br>`a:transform(e:ma)`<br>`ctrl-l,tab,right:transform(e:fw)`<br>`ctrl-h,left:transform(e:up)`<br>`/:transform(e:sl)`<br>`~:transform(e:hm)`<br>`j:down`<br>`k:up`<br>`h:trigger(ctrl-h)`<br>`l:trigger(tab)`<br>`q:abort`<br>`space:toggle` |
| Add | `enter:transform(e:en)`<br>`esc:transform(e:es)` | `enter:transform(e:en)`<br>`esc:transform(e:es)` |

Normal `i` enters Insert and Normal `a` enters Add. Insert keeps only the static Enter/Esc callbacks and the actual navigation bindings shown above; it does not bind `i`. Add has no normal or navigation bindings: Enter creates and loads the proposed directory tree rather than accepting picker output, while Esc returns to Normal.

Do not treat a displayed virtual record as a path: at a Windows drive or UNC share root, virtual `..` navigates to Drives, is neither previewable nor acceptable as filesystem output, and does not appear in Drives itself.

### zoxide policy

The public flags are `--zoxide-policy cached|fresh` and `--zoxide-timeout DURATION`. The default is `cached`, with a 75ms timeout on Linux and 150ms on Windows. These settings apply only to the eligible launch query that can add zoxide candidates to the initial CD view. A timeout of `0` means unlimited for that query, but only `--zoxide-policy fresh --zoxide-timeout 0` makes that launch query authoritative and unlimited.

`zoxide query --list` returns absolute filesystem paths. Accepted rows preserve arbitrary legal bytes in a valid platform-absolute path; a relative row is a malformed soft failure for the whole buffer, and virtual tokens never reach this check. Timeout and partial output are discarded.

For `cd`, zoxide candidates can appear only in the initial CD view. Cached and fresh launch queries each perform exactly one attempt and zero or one successful OS start, overlapped with local enumeration and with at most one live process per session Builder. Direct concurrent initial fresh calls remain permit-protected; a cancelled waiter returns promptly without factory or attempt, while independent sessions may query concurrently without a package-global mutex. After launch, navigation is local-only for every generation, including a return to the launch path: metrics report zoxide_outcome `not-run` and attempt, start, exit, process, and max-live counters are zero. Missing or spawn-failure on the eligible launch query means attempt 1/start 0. `cp` never invokes zoxide and has zero zoxide counters.

## Shell adapters

Source `adapters/zsh/shell-picker.plugin.zsh` and call `shell-picker-bind-zsh`; source `adapters/nushell/shell-picker.nu` and call `shell-picker-bind-nushell`. The shipped commands intentionally omit zoxide flags, so their eligible initial launch query uses the default cached policy and the 75ms Linux/150ms Windows timeout. Users who need that launch query to be authoritative must invoke the public fresh+`0` form directly; later navigation remains local-only.

The process topology is one parent and one fzf. Callback transforms are synchronous and short-lived. Optional preview tools are not required: native preview fallbacks remain useful when they are absent.

## Stream ownership

The cooperative stream contract is part of the process API. Direct `*os.File` streams are caller-owned and never closed by `Runner`. Non-file streams are pumped and must be finite, nonblocking, cooperative, or provide a nonblocking `Close` that promptly unblocks them. Eligible closers need stable pointer identity; a shared pointer is closed once only for emergency cleanup. A nonclosable stream blocked forever is outside the resource guarantee.

See [architecture](docs/architecture.md), [protocol](docs/protocol.md), [adapters](docs/adapters.md), [preview](docs/preview.md), [performance](docs/performance.md), [parity](docs/parity.md), and [security](docs/security.md). Licensed under the [MIT](LICENSE) license.
