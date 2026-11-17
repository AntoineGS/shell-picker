//go:build linux

package integration

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestZshRealFZFPathCompletion(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "bin")
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	plugin, err := filepath.Abs("../adapters/zsh/shell-picker.plugin.zsh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.cwd, "alpha", "document"), []byte("REAL-SHELL-PREVIEW"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(fixture.root, "state")
	rc := fmt.Sprintf(`PS1='READY> '
bindkey -e
autoload -Uz compinit
compinit -D
compdef _files nvim
source %q
shell-picker-bind-zsh
_capture() { print -rn -- "$BUFFER" > %q; zle -M CAPTURED; }
zle -N _capture
bindkey '^X^B' _capture
`, plugin, state)
	if err := os.WriteFile(filepath.Join(fixture.home, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(fixture.root, "shell")
	writeExecutable(t, launcher, "/bin/sh", fmt.Sprintf("exec %q -di >/dev/tty\n", zsh))
	term := newTerminalSession(t, terminalConfig{
		Path: launcher, Directory: fixture.cwd, Columns: 160, Lines: 45, DisablePickerTrace: true,
		Environment: replaceEnvironment(os.Environ(), "TERM=xterm-256color", "ZDOTDIR="+fixture.home,
			"PATH="+filepath.Dir(fixture.picker)+":"+filepath.Dir(fixture.fzf)+":"+os.Getenv("PATH"),
			"SHELL_PICKER_DISPLAY=auto", "_ZO_DATA_DIR="+fixture.home),
	})
	defer term.Close()
	waitForTerminalText(t, term, "READY> ")
	if err := term.Send([]byte("nvim al\t")); err != nil {
		t.Fatal(err)
	}
	ctx := testContext(t)
	for {
		output := term.Output()
		if bytes.Contains(output, []byte("\x1b[6n")) {
			break
		}
		term.WaitOutputAfter(ctx, len(output))
	}
	if err := term.Send([]byte("\x1b[3;1R")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "[I] al")
	if err := term.Send([]byte{9}); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "[I] alpha/")
	if err := term.Send([]byte("doc")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "REAL-SHELL-PREVIEW")
	beforeAccept := len(term.Output())
	if err := term.Send([]byte{13}); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeAccept, "nvim alpha/document")
	if err := term.Send([]byte{24, 2}); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeAccept, "CAPTURED")
	got, err := os.ReadFile(state)
	if err != nil || string(got) != "nvim alpha/document" {
		t.Fatalf("accepted command=%q err=%v", got, err)
	}
}
