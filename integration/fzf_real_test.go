package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzf"
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

type traceEvent struct {
	Schema         int    `json:"schema"`
	Session        string `json:"session"`
	Event          string `json:"event"`
	Generation     uint64 `json:"generation,omitempty"`
	CandidateCount int    `json:"candidate_count,omitempty"`
	Renderer       string `json:"renderer,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	Path           string `json:"path,omitempty"`
}

type barrier struct {
	Event      string
	Operation  string
	Generation uint64
	Count      int
}

type terminalSession interface {
	Send([]byte) error
	Resize(columns, lines uint16) error
	WaitBarrier(context.Context, barrier) traceEvent
	PID() int
	Output() []byte
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
	fixture.wantOutput = nil
	args := []string{string(picker), "--cwd", fixture.cwd, "--home", fixture.home, "--fzf", fixture.fzf,
		"--zoxide-policy", "cached", "--zoxide-timeout", "5ms"}
	environment := replaceEnvironment(os.Environ(),
		"FZF_DEFAULT_OPTS=--bind=start:abort", "FZF_DEFAULT_COMMAND=printf forged",
		"SHELL_PICKER_ADDR=http://127.0.0.1:1", "SHELL_PICKER_TOKEN=forged", "TERM=xterm-256color")
	environment = replaceEnvironment(environment, extraEnvironment...)
	return newTerminalSession(t, terminalConfig{Path: fixture.picker, Args: args, Environment: environment,
		Directory: fixture.cwd, Columns: 120, Lines: 35})
}

func (fixture *realFZFFixture) AssertAccepted(t *testing.T, term terminalSession, paths ...string) {
	t.Helper()
	want := make([]byte, 0)
	for _, path := range paths {
		want = append(want, path...)
		want = append(want, 0)
	}
	if !bytes.HasSuffix(term.Output(), want) {
		t.Fatalf("picker output does not end with %q: %q", want, term.Output())
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
	ldflags := "-X=main.helperPath=" + helper + " -X=main.controller=" + sentinel + " -X=main.subcommand=sentinel"
	build = exec.Command("go", "build", "-o", echo, "-ldflags", ldflags, "./integration/testhelper/delegate")
	build.Dir, build.Env = repository, append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build injection sentinel: %v\n%s", err, output)
	}
	path := fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	term := fixture.start(t, protocol.PickerCP, []string{"PATH=" + path})
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
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
	for _, line := range bytes.Split(term.Output(), []byte{'\n'}) {
		var event traceEvent
		if json.Unmarshal(line, &event) == nil && event.Event == "callback.event" &&
			!strings.Contains(" es ma mi fw up sl hm en ", " "+event.Outcome+" ") {
			t.Fatalf("untyped callback opcode in trace: %+v", event)
		}
	}
}

func TestRealFZFCPAcceptanceOrder(t *testing.T) {
	fixture := newRealFZFFixture(t, requireRealFZF(t), "ordered callback path with spaces")
	term := fixture.Start(t, protocol.PickerCP)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Count: 1})
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
