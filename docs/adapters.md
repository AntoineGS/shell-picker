# Adapter operations

The Zsh plugin binds Space: after `cd ` it launches the `cd` picker; Tab dispatches `cp` completion only when the parsed command position is appropriate. It uses Zsh parsing and `${(q)...}` quoting, preserves the buffer/cursor on failure, and inserts `--` before paths unless an existing `--` terminator was parsed. Uninstall by removing the source line and bindings.

The Nushell 0.113.1 adapter inserts Space at the exact cursor position, recognizes only `cd` or `cp`, invokes `%cd` inside `try`, parses NUON inside `try`, replaces `cd` input only after a successful directory change, and emits NUON-quoted `cp` paths with no trailing Space. Tab is unchanged. Windows backslash, spaces, and cross-volume `%cd` behavior are exercised rather than rewritten by the adapter.

The recommended low-latency Windows adapter is PowerShell 7.4.7+ with PSReadLine 2.3.6+. Install and register it with:

```powershell
Import-Module path\to\adapters\powershell\shell-picker.psd1
Register-ShellPicker -PickerPath path\to\shell-picker.exe
```

Ctrl+V and Shift+Insert use PSReadLine paste without invoking the per-character Space handler. Legacy right-click paste may invoke that handler and is a known limitation. Clink and Nushell remain supported, without a sub-100ms promise.

On Windows, fzf owns `--info=inline-right`; the adapter omits startup info/display callbacks, so count formatting and placement differ from Linux. Linux retains the exact custom `info-command` counts and transforms.

Shipped adapter invocations omit zoxide flags, so local-first streaming startup uses the default cached policy with 75ms on Linux and 150ms on Windows for its one asynchronous launch source. Zoxide candidates can appear only in the initial CD view; later navigation is local-only, including a return to the launch path. Use `--zoxide-policy fresh --zoxide-timeout 0` only when that initial launch query must be authoritative. The `cp` adapter never invokes zoxide, and shutdown joins any started source before returning.

CI runs Nushell on Linux and `windows-2025`. Its Windows case uses a dynamically selected unused drive from `Z:` through `D:` that differs from the starting volume, cleans a same-letter `subst`, and checks NUON quoting, cursor trigger, `%cd`, and unchanged Tab. No candidate, mapping failure, or cleanup failure fails that Windows job. Zsh remains Linux-only.

Real-fzf PTY/ConPTY tests automate Esc, `i`, `a`, navigation/reload, Add, Space, Enter, resize, accept, and abort. Physical cursor appearance in an individual terminal and integration with the outer ZLE/Reedline editor remain manual checks.

The fzf listen sidecar is experimental and requires explicit opt-in. No shell adapter injects or enables it. The protocol callback grammar remains the source of truth for callback transforms.
