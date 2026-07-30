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

```sh example
shell-picker cd --cwd "$PWD" --home "$HOME" --output nul
shell-picker cp --cwd "$PWD" --home "$HOME" --output nul
```

### Picker keys

These are user-facing fzf bindings, not callback opcodes: Enter accepts; Esc and `q` abort; `i` changes to insert mode; `a` starts Add; Ctrl-L, Tab, or Right navigates forward; Ctrl-H or Left goes to parent; `/` enters slash navigation; `~` goes home; `j`/`k` move selection and `h`/`l` trigger parent/forward. In `cd`, Space clears any mark and toggles the single selection; in `cp`, Space toggles marks. `cd` sorts and has one selectable item; `cp` preserves source order and supports multiple items. Add proposals are validated and created transactionally; abort produces no selected output. Internal callback opcodes are specified only in [the protocol reference](docs/protocol.md).

| Mode | Keys | Result |
| --- | --- | --- |
| Insert | `i`; Enter; Esc | `i` enters Insert; Enter accepts the current valid selection; Esc returns to Normal rather than aborting. |
| Normal | `a`, movement/navigation keys, Enter, `q` | `a` enters Add; Enter accepts; `q` is bound here and aborts fzf. |
| Add | Enter, Esc, navigation keys | Enter creates the proposed directory tree and navigates to it (it does not accept picker output); Esc cancels the Add proposal and returns to Normal. |

Do not treat a displayed virtual record as a path: at a Windows drive or UNC share root, virtual `..` navigates to Drives, is neither previewable nor acceptable as filesystem output, and does not appear in Drives itself.

### zoxide policy

The public flags are `--zoxide-policy cached|fresh` and `--zoxide-timeout DURATION`. The default is `cached`, with a 75ms timeout on Linux and 150ms on Windows. A timeout of `0` means unlimited for that query, but only `--zoxide-policy fresh --zoxide-timeout 0` supplies exact authoritative unlimited per-generation zoxide behavior.

`zoxide query --list` returns absolute filesystem paths. Accepted rows preserve arbitrary legal bytes in a valid platform-absolute path; a relative row is a malformed soft failure for the whole buffer, and virtual tokens never reach this check. Timeout and partial output are discarded.

For cached `cd`, there is exactly one invocation attempt per session, zero or one successful OS start, at most one live process, immutable cached later navigation, and no later attempts. Fresh `cd` performs exactly one attempt and zero or one successful start per completed generation, overlapped with local enumeration and with at most one live process per session Builder. A cancelled permit waiter returns promptly without factory or attempt; independent sessions may query concurrently without a package-global mutex. Missing or spawn-failure means attempt 1/start 0. `cp` makes zero attempts, starts, exits, and processes.

## Shell adapters

Source `adapters/zsh/shell-picker.plugin.zsh` and call `shell-picker-bind-zsh`; source `adapters/nushell/shell-picker.nu` and call `shell-picker-bind-nushell`. The shipped commands intentionally omit zoxide flags, so they use the default cached policy and the 75ms Linux/150ms Windows timeout. Users who need authoritative freshness must invoke the public fresh+`0` form directly.

The process topology is one parent and one fzf. Callback transforms are synchronous and short-lived. Optional preview tools are not required: native preview fallbacks remain useful when they are absent.

## Stream ownership

The cooperative stream contract is part of the process API. Direct `*os.File` streams are caller-owned and never closed by `Runner`. Non-file streams are pumped and must be finite, nonblocking, cooperative, or provide a nonblocking `Close` that promptly unblocks them. Eligible closers need stable pointer identity; a shared pointer is closed once only for emergency cleanup. A nonclosable stream blocked forever is outside the resource guarantee.

See [architecture](docs/architecture.md), [protocol](docs/protocol.md), [adapters](docs/adapters.md), [preview](docs/preview.md), [performance](docs/performance.md), [parity](docs/parity.md), and [security](docs/security.md). Licensed under the [MIT](LICENSE) license.
