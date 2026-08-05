package integration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	keyEsc   = []byte{0x1b}
	keyEnter = []byte{'\r'}
	keySpace = []byte{' '}
	keyLeft  = []byte{0x1b, '[', 'D'}
	keyDown  = []byte{0x1b, '[', 'B'}
)

type traceEvent = integrationpkg.TraceRecord

func decodeTraceEvent(data []byte) (traceEvent, error) {
	return integrationpkg.DecodeTraceRecordAt(data, time.Time{})
}

type barrier struct {
	Event      string
	Operation  string
	Renderer   string
	Generation uint64
	Count      int
}

func TestCallbackCompletionBarriersRequireSuccessfulCompletion(t *testing.T) {
	tests := []struct {
		name  string
		event traceEvent
		want  bool
	}{
		{name: "load error is not completion barrier", event: traceEvent{Event: "callback.load", Outcome: "error"}, want: false},
		{name: "load ok is completion barrier", event: traceEvent{Event: "callback.load", Outcome: "ok"}, want: true},
		{name: "start requires started", event: traceEvent{Event: "callback.load.start", Outcome: "started"}, want: true},
		{name: "fzf start uses successful outcome", event: traceEvent{Event: "fzf.start", Outcome: "ok"}, want: true},
		{name: "event operation remains explicit", event: traceEvent{Event: "callback.event", Outcome: "es"}, want: false},
		{name: "event operation matches", event: traceEvent{Event: "callback.event", Outcome: "es"}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wanted := barrier{Event: test.event.Event}
			if test.name == "event operation matches" {
				wanted.Operation = "es"
			}
			if got := matchesTraceBarrier(test.event, wanted); got != test.want {
				t.Fatalf("matchesTraceBarrier(%+v,%+v)=%v want %v", test.event, wanted, got, test.want)
			}
		})
	}
}

func matchesTraceBarrier(event traceEvent, wanted barrier) bool {
	if event.Event != wanted.Event || wanted.Renderer != "" && event.Renderer != wanted.Renderer ||
		wanted.Generation != 0 && event.Generation != wanted.Generation {
		return false
	}
	if wanted.Operation != "" {
		return event.Outcome == wanted.Operation
	}
	if strings.HasPrefix(event.Event, "callback.") && strings.HasSuffix(event.Event, ".start") {
		return event.Outcome == "started"
	}
	if strings.HasPrefix(event.Event, "callback.") {
		return event.Outcome == "ok"
	}
	return true
}

type terminalSession interface {
	Send([]byte) error
	Resize(columns, lines uint16) error
	WaitBarrier(context.Context, barrier) traceEvent
	TraceEvents() []traceEvent
	AssertProcessTopology(*testing.T)
	FZFCommandLine(*testing.T) string
	DescendantProcessRecords(*testing.T) []descendantProcessRecord
	DescendantCommandLines(*testing.T) []string
	TrackLiveDescendants(*testing.T) []trackedProcess
	AssertTrackedProcessesGone(*testing.T, []trackedProcess)
	PID() int
	Output() []byte
	ResultBytes() []byte
	WaitOutputAfter(context.Context, int)
	CloseInput() error
	Wait(context.Context) error
	Close() error
}

type trackedProcess struct {
	role     string
	identity ownedProcessIdentity
}

func registerTrackedProcesses(t *testing.T, tracked []trackedProcess) []trackedProcess {
	t.Helper()
	if len(tracked) == 0 {
		t.Fatal("picker had no live descendants to track")
	}
	t.Cleanup(func() {
		for _, process := range tracked {
			if err := process.identity.Close(); err != nil {
				t.Errorf("close tracked %s process %d: %v", process.role, process.identity.PID(), err)
			}
		}
	})
	return tracked
}

func assertTrackedProcessesGone(t *testing.T, tracked []trackedProcess) {
	t.Helper()
	for _, process := range tracked {
		if err := process.identity.WaitGone(testContext(t)); err != nil {
			t.Fatalf("tracked %s process %d remained live: %v", process.role, process.identity.PID(), err)
		}
	}
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

func realFZFSidecarEnabled(environment []string) bool {
	return fzfsidecar.Enabled(environment)
}

type realFZFFixture struct {
	root, cwd, home, picker, fzf string
	wantOutput                   []byte
}

func newRealFZFFixture(t *testing.T, fzfPath, executableDirectory string) *realFZFFixture {
	t.Helper()
	cachedPicker, _ := cachedRealBinaries(t)
	return newRealFZFFixtureWithPicker(t, fzfPath, executableDirectory, cachedPicker)
}

func newRealFZFFixtureWithPicker(t *testing.T, fzfPath, executableDirectory, pickerSource string) *realFZFFixture {
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
	picker := filepath.Join(bin, binaryName("shell-picker"))
	if err := os.Link(pickerSource, picker); err != nil {
		if err := copyExecutable(pickerSource, picker); err != nil {
			t.Fatalf("link cached public picker: %v", err)
		}
	}
	_ = os.Chmod(picker, 0o500)
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

func (fixture *realFZFFixture) AssertAccepted(t *testing.T, term terminalSession, paths ...string) {
	t.Helper()
	want := make([]byte, 0)
	for _, path := range paths {
		want = append(want, path...)
		want = append(want, 0)
	}
	output := term.ResultBytes()
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

func TestRealFZFInteractiveModesReloadAddAccept(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "directory with spaces")
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 1})
	term.AssertProcessTopology(t)
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	sendAndWait(t, term, []byte("a"), barrier{Event: "callback.event", Operation: "ma", Count: 1})
	if err := term.Send([]byte("created-dir")); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyEnter, barrier{Event: "generation.publish", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: 2})
	waitForTerminalText(t, term, "created-dir"+string(os.PathSeparator))
	beforeParent := len(term.Output())
	sendAndWait(t, term, keyLeft, barrier{Event: "generation.publish", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "callback.load", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 3})
	waitForTerminalTextAfter(t, term, beforeParent, filepath.Base(fixture.cwd)+string(os.PathSeparator))
	sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
	beforeQuery := len(term.Output())
	if err := term.Send([]byte("visiblex")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "[I] visiblex")
	waitForTerminalTextAfter(t, term, beforeQuery, "0/6")
	beforeFinalQuery := len(term.Output())
	if err := term.Send([]byte{0x7f}); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeFinalQuery, "[I] visible")
	waitForTerminalTextAfter(t, term, beforeFinalQuery, "1/6")
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
	if event.Outcome != "aborted" || len(term.ResultBytes()) != 0 {
		t.Fatalf("abort event/result=%+v/%q", event, term.ResultBytes())
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
	fakeBin := filepath.Join(fixture.root, "sentinel tools")
	_, helper := cachedRealBinaries(t)
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	echo := filepath.Join(fakeBin, "echo")
	if runtime.GOOS == "windows" {
		echo += ".exe"
	}
	ldflags := "-X=main.helperPath=" + helper + " -X=main.controller=" + sentinel + " -X=main.nonce=sentinel -X=main.subcommand=sentinel"
	build := exec.Command("go", "build", "-o", echo, "-ldflags", ldflags, "./integration/testhelper/delegate")
	build.Dir, build.Env = repository, append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build injection sentinel: %v\n%s", err, output)
	}
	path := fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	term := fixture.start(t, protocol.PickerCP, []string{"PATH=" + path})
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 1})
	term.AssertProcessTopology(t)
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	sendAndWait(t, term, []byte("a"), barrier{Event: "callback.event", Operation: "ma", Count: 1})
	if err := term.Send([]byte("created")); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyEnter, barrier{Event: "generation.publish", Generation: 2, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: 2})
	waitForTerminalText(t, term, "created"+string(os.PathSeparator))
	beforeParent := len(term.Output())
	sendAndWait(t, term, keyLeft, barrier{Event: "generation.publish", Generation: 3, Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 3})
	waitForTerminalTextAfter(t, term, beforeParent, filepath.Base(fixture.cwd)+string(os.PathSeparator))
	sendAndWait(t, term, []byte("i"), barrier{Event: "callback.event", Operation: "mi", Count: 1})
	beforeQuery := len(term.Output())
	if err := term.Send([]byte("visiblex")); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeQuery, "[I] visiblex")
	waitForTerminalTextAfter(t, term, beforeQuery, "0/4")
	beforeFinalQuery := len(term.Output())
	if err := term.Send([]byte{0x7f}); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeFinalQuery, "[I] visible")
	waitForTerminalTextAfter(t, term, beforeFinalQuery, "1/4")
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
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 1})
	term.AssertProcessTopology(t)
	beforeNormal := len(term.Output())
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 2})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Count: 2})
	waitForTerminalTextAfter(t, term, beforeNormal, "[N] ")
	for index, name := range []string{"..", "alpha", "visible"} {
		count := index + 2
		beforeDown := len(term.Output())
		if err := term.Send(keyDown); err != nil {
			t.Fatal(err)
		}
		term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: count})
		waitForTerminalTextAfter(t, term, beforeDown, "▌ "+name)
	}
	beforeFirstSelection := len(term.Output())
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeFirstSelection, "5/5 (1)")
	beforeUp := len(term.Output())
	if err := term.Send([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 5})
	waitForTerminalTextAfter(t, term, beforeUp, "▌ alpha")
	beforeSecondSelection := len(term.Output())
	if err := term.Send(keySpace); err != nil {
		t.Fatal(err)
	}
	waitForTerminalTextAfter(t, term, beforeSecondSelection, "5/5 (2)")
	if err := term.Send(keyEnter); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	fixture.AssertAccepted(t, term, "alpha", "visible")
}
