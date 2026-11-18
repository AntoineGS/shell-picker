//go:build !windows

package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func startCompletion(t *testing.T, fixture *realFZFFixture, query, display string, columns, lines uint16,
	extraEnvironment ...string) terminalSession {
	t.Helper()
	environment := replaceEnvironment(os.Environ(), "TERM=xterm-256color", "_ZO_DATA_DIR="+fixture.home,
		"XDG_DATA_HOME="+filepath.Join(fixture.home, ".local", "share"))
	environment = replaceEnvironment(environment, extraEnvironment...)
	return newTerminalSession(t, terminalConfig{
		Path: fixture.picker, Directory: fixture.cwd, Columns: columns, Lines: lines,
		Args: []string{"cp", "--cwd", fixture.cwd, "--home", fixture.home, "--fzf", fixture.fzf,
			"--completion", query, "--command-line", "nvim ", "--display", display},
		Environment: environment,
	})
}

func TestRealFZFCompletionEditablePaths(t *testing.T) {
	for _, key := range []struct {
		name  string
		bytes []byte
	}{
		{"tab", []byte{9}}, {"right", []byte("\x1b[C")}, {"ctrl-l", []byte{12}},
	} {
		t.Run(key.name, func(t *testing.T) {
			fixture := newRealFZFFixture(t, requireRealFZF(t), "completion")
			if err := os.WriteFile(filepath.Join(fixture.cwd, "alpha", "document"), []byte("COMPLETION-PREVIEW"), 0o600); err != nil {
				t.Fatal(err)
			}
			term := startCompletion(t, fixture, "al", "fullscreen", 160, 40)
			defer term.Close()
			defer func() {
				if t.Failed() {
					t.Logf("terminal: %s", visibleTerminalOutput(term.Output()))
					t.Logf("events: %+v", term.TraceEvents())
				}
			}()
			waitForTerminalText(t, term, "[I] al")
			sendAndWait(t, term, key.bytes, barrier{Event: "generation.publish", Generation: 2, Count: 1})
			waitForTerminalText(t, term, "[I] alpha/")
			if err := term.Send([]byte("doc")); err != nil {
				t.Fatal(err)
			}
			waitForTerminalText(t, term, "COMPLETION-PREVIEW")
			if err := term.Send([]byte{27}); err != nil {
				t.Fatal(err)
			}
			waitForTerminalText(t, term, "[N] alpha/doc")
			beforeInsert := len(term.Output())
			if err := term.Send([]byte("i")); err != nil {
				t.Fatal(err)
			}
			waitForTerminalTextAfter(t, term, beforeInsert, "[I] alpha/doc")
			if err := term.Send([]byte{13}); err != nil {
				t.Fatal(err)
			}
			if err := term.Wait(testContext(t)); err != nil {
				t.Fatal(err)
			}
			fixture.AssertAccepted(t, term, "alpha/document")
		})
	}
}

func TestRealFZFCompletionHomeDeletionReturnsToLaunch(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "completion home")
	indexEnvironment := fixture.indexFixture(t, nil)
	if err := os.WriteFile(filepath.Join(fixture.home, "home-only"), []byte("HOME-PREVIEW"), 0o600); err != nil {
		t.Fatal(err)
	}
	term := startCompletion(t, fixture, "", "fullscreen", 160, 40, indexEnvironment...)
	defer term.Close()
	waitForTerminalText(t, term, "[I]")
	before := len(term.Output())
	sendAndWait(t, term, []byte("~"), barrier{Event: "generation.publish", Generation: 2, Count: 1})
	waitForTerminalTextAfter(t, term, before, "home-only")
	before = len(term.Output())
	sendAndWait(t, term, []byte{127}, barrier{Event: "generation.publish", Generation: 3, Count: 1})
	waitForTerminalTextAfter(t, term, before, "alpha/")
	before = len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 1})
	waitForTerminalTextAfter(t, term, before, "nvim · "+expectedIndexedSourceLabel())
	before = len(term.Output())
	sendAndWait(t, term, []byte{0x06}, barrier{Event: "callback.event", Operation: "sf", Count: 2})
	waitForTerminalTextAfter(t, term, before, "alpha/")
	if err := term.Send([]byte("visible")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalText(t, term, "[I] visible")
	if err := term.Send([]byte{13}); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, "visible")
}

func TestRealFZFCompletionAutoDisplay(t *testing.T) {
	for _, test := range []struct {
		name   string
		lines  uint16
		inline bool
	}{
		{"roomy-inline", 45, true},
		{"short-fullscreen", 20, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealFZFFixture(t, requireRealFZF(t), "completion display")
			term := startCompletion(t, fixture, "visible", "auto", 160, test.lines)
			defer term.Close()
			if test.inline {
				ctx := testContext(t)
				for {
					output := term.Output()
					if bytes.Contains(output, []byte("\x1b[6n")) {
						break
					}
					term.WaitOutputAfter(ctx, len(output))
				}
				// Emulate the terminal's cursor-position report for fzf height mode.
				if err := term.Send([]byte("\x1b[3;1R")); err != nil {
					t.Fatal(err)
				}
			}
			waitForTerminalText(t, term, "[I] visible")
			if alternate := bytes.Contains(term.Output(), []byte("\x1b[?1049h")); alternate == test.inline {
				t.Fatalf("alternate screen=%t, inline=%t", alternate, test.inline)
			}
			if err := term.Send([]byte{13}); err != nil {
				t.Fatal(err)
			}
			if err := term.Wait(testContext(t)); err != nil {
				t.Fatal(err)
			}
			fixture.AssertAccepted(t, term, "visible")
		})
	}
}
