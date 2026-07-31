package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	keyCtrlA               = []byte{0x01}
	keyEsc                 = []byte{0x1b}
	keyEnter               = []byte{'\r'}
	keySpace               = []byte{' '}
	keyLeft                = []byte{0x1b, '[', 'D'}
	keyDown                = []byte{0x1b, '[', 'B'}
	terminalEscapeSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

type traceEvent struct {
	Schema          int    `json:"schema"`
	Time            string `json:"time"`
	Session         string `json:"session"`
	Event           string `json:"event"`
	Generation      uint64 `json:"generation,omitempty"`
	CandidateCount  int    `json:"candidate_count,omitempty"`
	Renderer        string `json:"renderer,omitempty"`
	Outcome         string `json:"outcome,omitempty"`
	Path            string `json:"path,omitempty"`
	ZoxidePolicy    string `json:"zoxide_policy,omitempty"`
	ZoxideOutcome   string `json:"zoxide_outcome,omitempty"`
	ZoxideAttempts  int    `json:"zoxide_attempts,omitempty"`
	ZoxideStarts    int    `json:"zoxide_starts,omitempty"`
	ZoxideExits     int    `json:"zoxide_exits,omitempty"`
	ZoxideProcesses int    `json:"zoxide_processes,omitempty"`
	ZoxideLive      int    `json:"zoxide_live,omitempty"`
	ZoxideMaxLive   int    `json:"zoxide_max_live,omitempty"`
	CallbackIPCUS   int64  `json:"callback_ipc_us,omitempty"`
	ChildStarts     int    `json:"child_starts,omitempty"`
	MaxLiveChildren int    `json:"max_live_children,omitempty"`
}

type barrier struct {
	Event      string
	Operation  string
	Renderer   string
	Generation uint64
	Count      int
}

type terminalSession interface {
	Send([]byte) error
	Resize(columns, lines uint16) error
	WaitBarrier(context.Context, barrier) traceEvent
	TraceEvents() []traceEvent
	AssertProcessTopology(*testing.T)
	PID() int
	Output() []byte
	WaitOutputAfter(context.Context, int)
	CloseInput() error
	Wait(context.Context) error
	Close() error
}

type terminalConfig struct {
	Path        string
	Args        []string
	Environment []string
	Directory   string
	Columns     uint16
	Lines       uint16
}

func requireRealFZF(t *testing.T) string {
	t.Helper()
	path := os.Getenv("SHELL_PICKER_REAL_FZF")
	if path == "" {
		t.Skip("set SHELL_PICKER_REAL_FZF to opt in")
	}
	if err := fzf.CheckVersion(context.Background(), process.Runner{}, path); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return absolute
}

type realFZFFixture struct {
	root, cwd, home, picker, fzf string
	wantOutput                   []byte
}

func newRealFZFFixture(t *testing.T, fzfPath, executableDirectory string) *realFZFFixture {
	t.Helper()
	root := t.TempDir()
	cwd := filepath.Join(root, "cwd")
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, executableDirectory)
	for _, directory := range []string{cwd, home, bin} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"alpha", "visible", "z-last"} {
		if err := os.Mkdir(filepath.Join(cwd, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	picker := filepath.Join(bin, "shell-picker")
	if runtime.GOOS == "windows" {
		picker += ".exe"
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", picker, "./cmd/shell-picker")
	command.Dir = repository
	command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build public picker: %v\n%s", err, output)
	}
	return &realFZFFixture{root: root, cwd: cwd, home: home, picker: picker, fzf: fzfPath}
}

func (fixture *realFZFFixture) Start(t *testing.T, picker protocol.Picker) terminalSession {
	t.Helper()
	return fixture.start(t, picker, nil)
}

func (fixture *realFZFFixture) start(t *testing.T, picker protocol.Picker, extraEnvironment []string) terminalSession {
	t.Helper()
	return fixture.startSized(t, picker, extraEnvironment, 120, 35)
}

func (fixture *realFZFFixture) startSized(t *testing.T, picker protocol.Picker, extraEnvironment []string,
	columns, lines uint16) terminalSession {
	t.Helper()
	fixture.wantOutput = nil
	args := []string{string(picker), "--cwd", fixture.cwd, "--home", fixture.home, "--fzf", fixture.fzf,
		"--zoxide-policy", "cached", "--zoxide-timeout", "5ms"}
	environment := replaceEnvironment(os.Environ(),
		"FZF_DEFAULT_OPTS=--bind=start:abort", "FZF_DEFAULT_COMMAND=printf forged",
		"SHELL_PICKER_ADDR=http://127.0.0.1:1", "SHELL_PICKER_TOKEN=forged", "TERM=xterm-256color")
	environment = replaceEnvironment(environment, extraEnvironment...)
	return newTerminalSession(t, terminalConfig{Path: fixture.picker, Args: args, Environment: environment,
		Directory: fixture.cwd, Columns: columns, Lines: lines})
}

func (fixture *realFZFFixture) AssertAccepted(t *testing.T, term terminalSession, paths ...string) {
	t.Helper()
	want := make([]byte, 0)
	for _, path := range paths {
		want = append(want, path...)
		want = append(want, 0)
	}
	output := term.Output()
	if bytes.Count(output, []byte{0}) != len(paths) || !bytes.HasSuffix(output, want) {
		t.Fatalf("picker output does not contain exactly %d accepted NUL records ending with %q: %q", len(paths), want, output)
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func sendAndWait(t *testing.T, term terminalSession, input []byte, wanted barrier) traceEvent {
	t.Helper()
	if err := term.Send(input); err != nil {
		t.Fatal(err)
	}
	return term.WaitBarrier(testContext(t), wanted)
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
		parentName := strings.Repeat("prefix-", 8)
		fixture.cwd = filepath.Join(fixture.cwd, parentName, "rightmost-location")
		for _, directory := range []string{fixture.cwd, filepath.Join(fixture.cwd, "alpha"), filepath.Join(fixture.cwd, "beta")} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		term := fixture.startSized(t, protocol.PickerCP, nil, 64, 24)
		defer term.Close()
		term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
		waitForTerminalText(t, term, "rightmost-location"+string(os.PathSeparator))
		waitForTerminalText(t, term, "[I] ")
		if bytes.Contains(visibleTerminalOutput(term.Output()), []byte("[I] "+fixture.cwd)) {
			t.Fatalf("location rendered in input line: %q", term.Output())
		}

		query := "query-begin-abcdefghijklmnopqrstuvwxyz-query-end"
		if err := term.Send([]byte(query)); err != nil {
			t.Fatal(err)
		}
		waitForTerminalText(t, term, "query-end")
		beforeCtrlA := len(term.Output())
		if err := term.Send(keyCtrlA); err != nil {
			t.Fatal(err)
		}
		waitForTerminalTextAfter(t, term, beforeCtrlA, "query-begin")

		beforeResize := len(term.Output())
		if err := term.Resize(48, 24); err != nil {
			t.Fatal(err)
		}
		waitForTerminalTextAfter(t, term, beforeResize, "most-location")

		retainedPathTail := "most-location" + string(os.PathSeparator)
		beforeNormal := len(term.Output())
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
		waitForTerminalTextAfter(t, term, beforeNormal, "[N] ")
		assertModePathSeparated(t, term, beforeNormal, "[N] ", fixture.cwd, retainedPathTail)
		beforeAdd := len(term.Output())
		sendAndWait(t, term, []byte("a"), barrier{Event: "callback.event", Operation: "ma", Count: 1})
		waitForTerminalTextAfter(t, term, beforeAdd, "[A] ")
		assertModePathSeparated(t, term, beforeAdd, "[A] ", fixture.cwd, retainedPathTail)
		if err := term.Send([]byte("../invalid")); err != nil {
			t.Fatal(err)
		}
		beforeAddError := len(term.Output())
		sendAndWait(t, term, keyEnter, barrier{Event: "callback.event", Operation: "en", Count: 1})
		waitForTerminalTextAfter(t, term, beforeAddError, "[A!] ")
		assertModePathSeparated(t, term, beforeAddError, "[A!] ", fixture.cwd, retainedPathTail)
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 2})
		beforeInsert := len(term.Output())
		sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
		waitForTerminalTextAfter(t, term, beforeInsert, "[I] ")
		assertModePathSeparated(t, term, beforeInsert, "[I] ", fixture.cwd, retainedPathTail)
		sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 3})
		beforeNavigation := len(term.Output())
		sendAndWait(t, term, keyLeft, barrier{Event: "generation.publish", Generation: 2, Count: 1})
		waitForTerminalTextAfter(t, term, beforeNavigation, "prefix-prefix-")
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
	waitForTerminalText(t, term, "3/3")
	if bytes.Contains(visibleTerminalOutput(term.Output()), []byte("(0)")) {
		t.Fatalf("zero selection count rendered: %q", term.Output())
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	beforeSelection := len(term.Output())
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyDown, barrier{Event: "preview.dispatch", Count: 2})
	if wantSelected {
		waitForTerminalTextAfter(t, term, beforeSelection, "3/3 (1)")
	} else {
		waitForTerminalTextAfter(t, term, beforeSelection, "3/3")
	}
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

func TestRealFZFInteractiveModesReloadAddAccept(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "directory with spaces")
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	sendAndWait(t, term, []byte("a"), barrier{Event: "callback.event", Operation: "ma", Count: 1})
	if err := term.Send([]byte("created-dir")); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyEnter, barrier{Event: "generation.publish", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	sendAndWait(t, term, keyLeft, barrier{Event: "generation.publish", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 3})
	sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
	if err := term.Send([]byte("visiblx")); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte{0x7f, 'e'}); err != nil {
		t.Fatal(err)
	}
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, "visible")
	if bytes.Contains(term.Output(), []byte("forged")) {
		t.Fatalf("inherited fzf defaults reached session: %q", term.Output())
	}
}

func TestRealFZFInteractiveAbort(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "callback path with spaces")
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	event := term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	if event.Outcome != "aborted" || bytes.Contains(term.Output(), []byte{0}) {
		t.Fatalf("abort event/output=%+v/%q", event, term.Output())
	}
}

func TestRealFZFAdversarialPromptCannotInjectAction(t *testing.T) {
	corpus := `x)+execute(echo injected)+change-prompt(\\,,: spaced`
	fixture := newRealFZFFixture(t, requireRealFZF(t), "adversarial callback path with spaces")
	pathCorpus := corpus
	if runtime.GOOS == "windows" {
		// The native absolute path supplies backslashes and a drive colon; neither is legal within a Windows component.
		pathCorpus = `x)+execute(echo injected)+change-prompt(,, spaced`
	}
	fixture.cwd = filepath.Join(fixture.cwd, pathCorpus)
	if err := os.MkdirAll(filepath.Join(fixture.cwd, "visible"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(fixture.root, "injected")
	helper := filepath.Join(fixture.root, "sentinel-helper")
	fakeBin := filepath.Join(fixture.root, "sentinel tools")
	if runtime.GOOS == "windows" {
		helper += ".exe"
	}
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", helper, "./integration/testhelper")
	build.Dir, build.Env = repository, append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sentinel helper: %v\n%s", err, output)
	}
	echo := filepath.Join(fakeBin, "echo")
	if runtime.GOOS == "windows" {
		echo += ".exe"
	}
	ldflags := "-X=main.helperPath=" + helper + " -X=main.controller=" + sentinel + " -X=main.nonce=sentinel -X=main.subcommand=sentinel"
	build = exec.Command("go", "build", "-o", echo, "-ldflags", ldflags, "./integration/testhelper/delegate")
	build.Dir, build.Env = repository, append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build injection sentinel: %v\n%s", err, output)
	}
	path := fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	term := fixture.start(t, protocol.PickerCP, []string{"PATH=" + path})
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	sendAndWait(t, term, []byte("a"), barrier{Event: "callback.event", Operation: "ma", Count: 1})
	if err := term.Send([]byte("created")); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyEnter, barrier{Event: "generation.publish", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	sendAndWait(t, term, keyLeft, barrier{Event: "generation.publish", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 3})
	sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
	if err := term.Send([]byte("visible")); err != nil {
		t.Fatal(err)
	}
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, "visible")
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("action injection sentinel exists: %v", err)
	}
	var callbacks []string
	for _, event := range term.TraceEvents() {
		if event.Event == "callback.event" {
			callbacks = append(callbacks, event.Outcome)
		}
	}
	wantCallbacks := []string{"es", "ma", "en", "up", "mi", "en"}
	if !reflect.DeepEqual(callbacks, wantCallbacks) {
		t.Fatalf("callback opcodes=%q want %q; events=%+v", callbacks, wantCallbacks, term.TraceEvents())
	}
	if output := strings.ToLower(string(term.Output())); strings.Contains(output, "callback usage") ||
		strings.Contains(output, "parse callback") || strings.Contains(output, "invalid callback") {
		t.Fatalf("callback parse/usage error: %q", term.Output())
	}
}

func TestRealFZFCPAcceptanceOrder(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "ordered callback path with spaces")
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
	term.AssertProcessTopology(t)
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	for count := 2; count <= 4; count++ {
		if err := term.Send(keyDown); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: count})
	}
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 5})
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, "alpha", "visible")
}
