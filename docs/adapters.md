# Adapter operations

The Zsh plugin binds Space: after `cd ` it launches the `cd` picker; Tab dispatches `cp` completion only when the parsed command position is appropriate. It uses Zsh parsing and `${(q)...}` quoting, preserves the buffer/cursor on failure, and inserts `--` before paths unless an existing `--` terminator was parsed. Uninstall by removing the source line and bindings.

The Nushell 0.113.1 adapter inserts Space at the exact cursor position, recognizes only `cd` or `cp`, invokes `%cd` inside `try`, parses NUON inside `try`, replaces `cd` input only after a successful directory change, and emits NUON-quoted `cp` paths with no trailing Space. Tab is unchanged. Windows backslash, spaces, and cross-volume `%cd` behavior are exercised rather than rewritten by the adapter.

Shipped adapter invocations omit zoxide flags, so the eligible launch query uses the default cached policy with 75ms on Linux and 150ms on Windows. Zoxide candidates can appear only in the initial CD view; later navigation is local-only, including a return to the launch path. Use `--zoxide-policy fresh --zoxide-timeout 0` only when that initial launch query must be authoritative. The `cp` adapter never invokes zoxide.

CI runs Nushell on Linux and `windows-2025`. Its Windows case uses a dynamically selected unused drive from `Z:` through `D:` that differs from the starting volume, cleans a same-letter `subst`, and checks NUON quoting, cursor trigger, `%cd`, and unchanged Tab. No candidate, mapping failure, or cleanup failure fails that Windows job. Zsh remains Linux-only.

Real-fzf PTY/ConPTY tests automate Esc, `i`, `a`, navigation/reload, Add, Space, Enter, resize, accept, and abort. Physical cursor appearance in an individual terminal and integration with the outer ZLE/Reedline editor remain manual checks.
