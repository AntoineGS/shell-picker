# Adapter operations

The Zsh plugin leaves Space as `magic-space` and binds Tab in Emacs and vi Insert
keymaps. A native completion widget probes `_main_complete` in a subshell, recognizes
path contexts from `_path_files` and `compadd -f`, and delegates non-path contexts
to `fzf_completion`. File-valued options retain their ignored prefixes/suffixes
(for example `--output=`). Native `compadd` replaces the active word and preserves
surrounding arguments, including text to the right of the cursor.

`cd`, `pushd`, and commands registered with zoxide's `__zoxide_z_complete` use the
directory picker directly, avoiding zoxide's interactive completion helper and its
`accept-line` action. The user's command is retained; accepting a picker result
only inserts a path. The backend supplies local and initial zoxide candidates and
formats the result relative to the launch directory unless the input starts with
`/` or `~`. Leading-dash relative paths receive `./` rather than injecting a
command-specific `--` argument.

Both `shell-picker.plugin.zsh` and `shell-picker-completion.zsh` must be installed
in the same directory. The retained explicit `_shell_picker_cd` and
`_shell_picker_cp` widgets preserve their legacy behavior for custom bindings;
the default Tab binding does not call them. Uninstall by removing the source line
and bindings.

Completion starts with local candidates while zoxide loads. A first navigation
or source switch waits for the pending zoxide result before saving the launch
view, bounded by the remaining configured zoxide timeout (75 ms by default on
Linux). Ordinary filename edits and acceptance do not wait. An explicitly
unlimited timeout (`--zoxide-timeout 0`) also makes this navigation wait unlimited;
canceling the session still stops the source.

The Zsh Ctrl+F widget invokes `shell-picker search` outside a picker and
switches sources inside cd/cp. Standalone search inserts shell-quoted absolute
paths at the cursor; it does not execute the line or add cp's `--` prefix.
FSearch data comes from the last saved database snapshot, so recent filesystem
changes may not be present. Missing, busy, unsupported, or malformed indexes
are reported without discarding the current command buffer.
For safe insertion, the widget declines to launch when the cursor is inside an
open quote, after a dangling escape, or inside a shell-expansion context;
`BUFFER` and `CURSOR` remain unchanged in those cases.

Ctrl+Shift+F opens files-only search at the prompt and
inside a picker, even in `cd`. In Ghostty, use
`keybind = ctrl+shift+f=text:\x1b\x06`; fzf names that sequence `ctrl-alt-f`,
which also works directly. Accepting this forced search inserts quoted absolute
paths without executing the command. Ctrl+F returns to the saved browser view,
including its query, marks, and any zoxide entries already received.
The files-only header includes `FSearch · files only`; directories are excluded
from the candidate set. The Zsh shortcut uses `search --search-scope files`;
ordinary Ctrl+F retains its context-sensitive behavior, and `search --search-scope
all` includes both files and directories.
Ctrl+H and Left both go to the parent in browser Insert and Normal modes.

The Nushell 0.113.1 adapter inserts Space at the exact cursor position, recognizes only `cd` or `cp`, invokes `%cd` inside `try`, parses NUON inside `try`, replaces `cd` input only after a successful directory change, and emits NUON-quoted `cp` paths with no trailing Space. Tab is unchanged. Windows backslash, spaces, and cross-volume `%cd` behavior are exercised rather than rewritten by the adapter.

The recommended low-latency Windows adapter is PowerShell 7.4.7+ with PSReadLine 2.3.6+. Install and register it with:

```powershell
Import-Module path\to\adapters\powershell\shell-picker.psd1
Register-ShellPicker -PickerPath path\to\shell-picker.exe
```

Ctrl+V and Shift+Insert use PSReadLine paste without invoking the per-character Space handler. Legacy right-click paste may invoke that handler and is a known limitation. Clink and Nushell remain supported, without a sub-100ms promise.

On Windows, fzf owns `--info=inline-right`; the adapter omits startup info/display callbacks, so count formatting and placement differ from Linux. Linux retains the exact custom `info-command` counts and transforms.

Shipped adapter invocations omit zoxide flags, so local-first streaming startup uses the default cached policy with 75ms on Linux and 150ms on Windows for its one asynchronous launch source. Zoxide candidates appear in the initial CD view. Editable-path completion restores that saved candidate set when returning to the launch directory; legacy browser navigation remains local-only. Use `--zoxide-policy fresh --zoxide-timeout 0` only when that initial launch query must be authoritative. The `cp` adapter never invokes zoxide, and shutdown joins any started source before returning.

CI runs Nushell on Linux and `windows-2025`. Its Windows case uses a dynamically selected unused drive from `Z:` through `D:` that differs from the starting volume, cleans a same-letter `subst`, and checks NUON quoting, cursor trigger, `%cd`, and unchanged Tab. No candidate, mapping failure, or cleanup failure fails that Windows job. Zsh remains Linux-only.

Real-fzf PTY/ConPTY tests automate Esc, `i`, `a`, navigation/reload, Add, Space, Enter, resize, accept, and abort. Linux PTY tests also exercise ZLE path completion through inline fzf and command-line insertion. Physical cursor appearance in an individual terminal and integration with the outer Reedline editor remain manual checks.

The fzf listen sidecar is experimental and requires explicit opt-in. No shell adapter injects or enables it. The protocol callback grammar remains the source of truth for callback transforms.
