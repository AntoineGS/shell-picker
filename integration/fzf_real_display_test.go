package integration

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	keyCtrlA               = []byte{0x01}
	terminalEscapeSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

func (fixture *realFZFFixture) startSized(t *testing.T, picker protocol.Picker, extraEnvironment []string,
	columns, lines uint16) terminalSession {
	return fixture.startSizedWithZoxideTimeout(t, picker, extraEnvironment, columns, lines, "5ms")
}

func (fixture *realFZFFixture) startSizedWithZoxideTimeout(t *testing.T, picker protocol.Picker,
	extraEnvironment []string, columns, lines uint16, timeout string) terminalSession {
	t.Helper()
	fixture.wantOutput = nil
	args := []string{string(picker), "--cwd", fixture.cwd, "--home", fixture.home, "--fzf", fixture.fzf,
		"--zoxide-policy", "cached", "--zoxide-timeout", timeout}
	environment := replaceEnvironment(os.Environ(),
		"FZF_DEFAULT_OPTS=--bind=start:abort", "FZF_DEFAULT_COMMAND=printf forged",
		"SHELL_PICKER_ADDR=http://127.0.0.1:1", "SHELL_PICKER_TOKEN=forged", "TERM=xterm-256color")
	environment = replaceEnvironment(environment, extraEnvironment...)
	return newTerminalSession(t, terminalConfig{Path: fixture.picker, Args: args, Environment: environment,
		Directory: fixture.cwd, Columns: columns, Lines: lines})
}

func waitForTerminalText(t *testing.T, term terminalSession, text string) {
	t.Helper()
	ctx := testContext(t)
	for {
		output := term.Output()
		if bytes.Contains(terminalEscapeSequence.ReplaceAll(output, nil), []byte(text)) {
			return
		}
		term.WaitOutputAfter(ctx, len(output))
	}
}

func waitForTerminalTextAfter(t *testing.T, term terminalSession, before int, text string) {
	t.Helper()
	ctx := testContext(t)
	for {
		output := term.Output()
		if before <= len(output) &&
			bytes.Contains(terminalEscapeSequence.ReplaceAll(output[before:], nil), []byte(text)) {
			return
		}
		term.WaitOutputAfter(ctx, len(output))
	}
}

func visibleTerminalOutput(output []byte) []byte {
	return terminalEscapeSequence.ReplaceAll(output, nil)
}

func assertModePathSeparated(t *testing.T, term terminalSession, before int, mode, fullPath, retainedTail string) {
	t.Helper()
	output := term.Output()
	if before > len(output) {
		t.Fatalf("mode output offset %d exceeds terminal output length %d", before, len(output))
	}
	visible := visibleTerminalOutput(output[before:])
	for _, path := range []string{fullPath, retainedTail} {
		if bytes.Contains(visible, []byte(mode+path)) {
			t.Fatalf("location %q rendered adjacent to %s input marker: %q", path, mode, output[before:])
		}
	}
}

func TestRealFZFTwoLineDisplayAndConditionalSelectionInfo(t *testing.T) {
	t.Run("narrow display preserves right tails across interaction", func(t *testing.T) {
		fixture := newRealFZFFixture(t, requireRealFZF(t), "two-line display")
		parentNames := []string{
			"header-prefix-01", "header-prefix-02", "header-prefix-03", "header-prefix-04",
			"header-prefix-05", "header-prefix-06", "header-prefix-07", "header-prefix-08",
		}
		fixture.cwd = filepath.Join(append([]string{fixture.cwd}, append(parentNames, "rightmost-location")...)...)
		for _, directory := range []string{fixture.cwd, filepath.Join(fixture.cwd, "alpha"), filepath.Join(fixture.cwd, "beta")} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		const query = "query-begin-abcdefghijklmnopqrstuvwxyz-query-end"
		const stablePreviewMarker = "STABLE-RESIZE-PREVIEW"
		if err := os.WriteFile(filepath.Join(fixture.cwd, query+"-stable-preview.txt"), []byte(stablePreviewMarker+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		term := fixture.startSized(t, protocol.PickerCP, nil, 200, 35)
		defer term.Close()
		term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
		sideBySideHeaderTail := filepath.Join(parentNames[4:]...) + string(os.PathSeparator) + "rightmost-location" + string(os.PathSeparator)
		longerRetainedHeaderTail := filepath.Join(parentNames[3:]...) + string(os.PathSeparator) + "rightmost-location" + string(os.PathSeparator)
		waitForTerminalText(t, term, sideBySideHeaderTail)
		waitForTerminalText(t, term, "[I] ")
		assertModePathSeparated(t, term, 0, "[I] ", fixture.cwd, sideBySideHeaderTail)
		if visible := visibleTerminalOutput(term.Output()); bytes.Contains(visible, []byte(longerRetainedHeaderTail)) {
			t.Fatalf("side-by-side header retained the stacked tail %q: %q", longerRetainedHeaderTail, visible)
		}

		if err := term.Send([]byte(query)); err != nil {
			t.Fatal(err)
		}
		waitForTerminalText(t, term, "query-end")
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
		waitForTerminalText(t, term, stablePreviewMarker)
		beforeCtrlA := len(term.Output())
		if err := term.Send(keyCtrlA); err != nil {
			t.Fatal(err)
		}
		waitForTerminalTextAfter(t, term, beforeCtrlA, "query-begin")

		beforeResize := len(term.Output())
		if err := term.Resize(120, 35); err != nil {
			t.Fatal(err)
		}
		term.WaitOutputAfter(testContext(t), beforeResize)
		waitForTerminalTextAfter(t, term, beforeResize, longerRetainedHeaderTail)
		waitForTerminalTextAfter(t, term, beforeResize, stablePreviewMarker)
		if len(longerRetainedHeaderTail) <= len(sideBySideHeaderTail) {
			t.Fatalf("post-resize header tail %q is not longer than side-by-side tail %q", longerRetainedHeaderTail, sideBySideHeaderTail)
		}

		retainedPathTail := "rightmost-location" + string(os.PathSeparator)
		beforeNormal := len(term.Output())
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
		waitForTerminalTextAfter(t, term, beforeNormal, "[N]")
		assertModePathSeparated(t, term, beforeNormal, "[N] ", fixture.cwd, retainedPathTail)
		beforeAdd := len(term.Output())
		sendAndWait(t, term, []byte("a"), barrier{Event: "callback.event", Operation: "ma", Count: 1})
		waitForTerminalTextAfter(t, term, beforeAdd, "[A]")
		assertModePathSeparated(t, term, beforeAdd, "[A] ", fixture.cwd, retainedPathTail)
		if err := term.Send([]byte("../invalid")); err != nil {
			t.Fatal(err)
		}
		beforeAddError := len(term.Output())
		sendAndWait(t, term, keyEnter, barrier{Event: "callback.event", Operation: "en", Count: 1})
		waitForTerminalTextAfter(t, term, beforeAddError, "[A!]")
		assertModePathSeparated(t, term, beforeAddError, "[A!] ", fixture.cwd, retainedPathTail)
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 2})
		beforeInsert := len(term.Output())
		sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
		waitForTerminalTextAfter(t, term, beforeInsert, "[I]")
		assertModePathSeparated(t, term, beforeInsert, "[I] ", fixture.cwd, retainedPathTail)
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 3})
		beforeNavigation := len(term.Output())
		sendAndWait(t, term, keyLeft, barrier{Event: "generation.publish", Generation: 2, Count: 1})
		waitForTerminalTextAfter(t, term, beforeNavigation, "header-prefix-")
	})

	t.Run("narrow candidate preserves rightmost path component", func(t *testing.T) {
		fixture := newRealFZFFixture(t, requireRealFZF(t), "narrow candidate")
		source, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		tools := t.TempDir()
		name := "zoxide"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := copyExecutable(source, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
		longParent := filepath.Join(fixture.home, strings.Repeat("candidate-prefix-", 8))
		for _, name := range []string{"visible", "zoxide-one", "zoxide-two"} {
			if err := os.MkdirAll(filepath.Join(longParent, name), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		absoluteTarget := filepath.Join(longParent, "zoxide-one")
		environment := []string{
			"PATH=" + tools,
			parityHelperEnvironment + "=zoxide-ok",
			"PARITY_TEST_ROOT=" + longParent,
		}
		term := fixture.startSizedWithZoxideTimeout(t, protocol.PickerCD, environment, 48, 24, "30s")
		defer term.Close()
		term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
		beforeFilter := len(term.Output())
		previewBefore := traceCount(term.TraceEvents(), "preview.dispatch", "")
		if err := term.Send([]byte("zoxide-one")); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: previewBefore + 1})
		term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: previewBefore + 1})
		waitForTerminalTextAfter(t, term, beforeFilter, "[I] zoxide-one")
		if bytes.Contains(visibleTerminalOutput(term.Output()), []byte(strings.Repeat("candidate-prefix-", 8))) {
			t.Fatalf("candidate row retained its leftmost relative prefix: %q", term.Output())
		}
		if err := term.Send(keyEnter); err != nil {
			t.Fatal(err)
		}
		if err := term.Wait(testContext(t)); err != nil {
			t.Fatal(err)
		}
		fixture.AssertAccepted(t, term, absoluteTarget)
	})

	for _, test := range []struct {
		name         string
		picker       protocol.Picker
		wantSelected bool
	}{
		{name: "CD omits selection count", picker: protocol.PickerCD},
		{name: "CP shows selection count", picker: protocol.PickerCP, wantSelected: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealFZFFixture(t, requireRealFZF(t), "selection info")
			for _, name := range []string{"visible", "z-last"} {
				if err := os.Remove(filepath.Join(fixture.cwd, name)); err != nil {
					t.Fatal(err)
				}
			}
			assertRealFZFSelectionInfo(t, fixture, test.picker, test.wantSelected)
		})
	}
}

func assertRealFZFSelectionInfo(t *testing.T, fixture *realFZFFixture, picker protocol.Picker, wantSelected bool) {
	t.Helper()
	term := fixture.start(t, picker, []string{"PATH=" + t.TempDir()})
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 1})
	waitForTerminalText(t, term, "3/3")
	beforeNormal := len(term.Output())
	loadCount := traceCountGeneration(term.TraceEvents(), "callback.load", 1)
	finishedCount := traceCount(term.TraceEvents(), "preview.finished", "")
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	waitForTerminalTextAfter(t, term, beforeNormal, "[N]")
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 1, Count: loadCount + 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: finishedCount + 1})
	if bytes.Contains(visibleTerminalOutput(term.Output()), []byte("(0)")) {
		t.Fatalf("zero selection count rendered: %q", term.Output())
	}
	beforeSelection := len(term.Output())
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	if wantSelected {
		waitForTerminalTextAfter(t, term, beforeSelection, "(1)")
	} else {
		term.WaitOutputAfter(testContext(t), beforeSelection)
	}
	beforeInsert := len(term.Output())
	sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
	waitForTerminalTextAfter(t, term, beforeInsert, "[I]")
	output := term.Output()
	selectedOutput := visibleTerminalOutput(output[beforeSelection:])
	if bytes.Contains(selectedOutput, []byte("(0)")) {
		t.Fatalf("zero selection count rendered after active selection: %q", output[beforeSelection:])
	}
	if wantSelected && !bytes.Contains(selectedOutput, []byte("(1)")) {
		t.Fatalf("CP selection count missing: %q", output[beforeSelection:])
	}
	if !wantSelected && bytes.Contains(selectedOutput, []byte("(1)")) {
		t.Fatalf("CD selection count rendered: %q", term.Output())
	}
}
