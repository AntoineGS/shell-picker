//go:build linux

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestZshPathCompletion(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	plugin, err := filepath.Abs("../adapters/zsh/shell-picker.plugin.zsh")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, input, selected, want, picker string
		fallback                            bool
	}{
		{"ls", "ls ", "alpha file", `ls alpha\ file`, "cp", false},
		{"editor-partial", "nvim al", "alpha file", `nvim alpha\ file`, "cp", false},
		{"cd", "cd ", "alpha dir", `cd alpha\ dir`, "cd", false},
		{"zoxide", "jump project ", "alpha dir", `jump project alpha\ dir`, "cd", false},
		{"tilde", "nvim ~/", "~/notes/a b", `nvim ~/notes/a\ b`, "cp", false},
		{"quoted", `nvim "al`, `alpha"file`, `nvim "alpha\"file`, "cp", false},
		{"escaped-space", `nvim alpha\ f`, "alpha file", `nvim alpha\ file`, "cp", false},
		{"option-path", "myopts --output=", "alpha file", `myopts --output=alpha\ file`, "cp", false},
		{"enum", "myopts --format=", "", "myopts --format=", "", true},
		{"flag", "ls --col", "", "ls --col", "", true},
		{"command", "nv", "", "nv", "", true},
		{"pipeline", "printf test | nvim al", "alpha file", `printf test | nvim alpha\ file`, "cp", false},
		{"middle-word", "nvim alpha tail\x01" + strings.Repeat("\x1b[C", 7), "alpha file", `nvim alpha\ file tail`, "cp", false},
		{"cancel", "nvim al", "", "nvim al", "cp", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			output, calls, state := filepath.Join(root, "output"), filepath.Join(root, "calls"), filepath.Join(root, "state")
			if err := os.WriteFile(output, []byte(test.selected+"\x00"), 0o600); err != nil {
				t.Fatal(err)
			}
			writeExecutable(t, filepath.Join(bin, "shell-picker"), zsh, `
print -rn -- "$1"$'\0' >> "$PICKER_CALLS"
cat -- "$PICKER_OUTPUT"
`)
			launcher := filepath.Join(root, "shell")
			writeExecutable(t, launcher, "/bin/sh", fmt.Sprintf("export PS1='READY> '\nexec %q -dfi >/dev/tty\n", zsh))
			term := newTerminalSession(t, terminalConfig{
				Path: launcher, Directory: root, Columns: 160, Lines: 40, DisablePickerTrace: true,
				Environment: replaceEnvironment(os.Environ(), "TERM=xterm-256color", "PS1=READY> ",
					"ZDOTDIR="+root,
					"PATH="+bin+":"+os.Getenv("PATH"), "PICKER_CALLS="+calls, "PICKER_OUTPUT="+output, "PICKER_STATE="+state),
			})
			defer term.Close()
			defer func() {
				if t.Failed() {
					t.Logf("setup output=%q result=%q", term.Output(), term.ResultBytes())
				}
			}()
			waitForTerminalText(t, term, "READY> ")
			setup := fmt.Sprintf(`autoload -Uz compinit; compinit -D; source %q; fzf_completion() { print fallback >> "$PICKER_CALLS"; }; zle -N fzf_completion; _myopts() { _arguments '--format=[format]:format:(json yaml)' '--output=[file]:file:_files' '*:file:_files'; }; compdef _myopts myopts; compdef _files nvim; __zoxide_z_complete() { print unexpected-zoxide >> "$PICKER_CALLS"; }; compdef __zoxide_z_complete jump; _capture() { print -rn -- "$BUFFER" > "$PICKER_STATE"; zle -M CAPTURED; }; zle -N _capture; bindkey '^X^B' _capture; shell-picker-bind-zsh; print -u2 SETUP-DONE`, plugin)
			setupBefore := len(term.Output())
			if err := term.Send([]byte("bindkey -e; " + setup + "\n")); err != nil {
				t.Fatal(err)
			}
			waitForTerminalText(t, term, "\r\nSETUP-DONE\r\n")
			waitForTerminalTextAfter(t, term, setupBefore, "READY> ")
			before := len(term.Output())
			if err := term.Send([]byte(test.input + "\t\x18\x02")); err != nil {
				t.Fatal(err)
			}
			waitForTerminalTextAfter(t, term, before, "CAPTURED")
			got, err := os.ReadFile(state)
			if err != nil || string(got) != test.want {
				t.Fatalf("buffer=%q want=%q err=%v\nterminal=%s", got, test.want, err, visibleTerminalOutput(term.Output()))
			}
			launched, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := test.picker + "\x00"
			if test.fallback {
				want = "fallback\n"
			}
			if string(launched) != want {
				t.Fatalf("calls=%q want=%q", launched, want)
			}
		})
	}
}
