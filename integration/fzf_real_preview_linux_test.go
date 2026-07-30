//go:build linux

package integration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"golang.org/x/sys/unix"
)

type previewController struct {
	t        *testing.T
	listener net.Listener
	mu       sync.Mutex
	events   []controlEvent
	changed  chan struct{}
	clients  map[int]net.Conn
	closed   chan struct{}
	nonce    string
}

func newPreviewController(t *testing.T) *previewController {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rawNonce := make([]byte, 16)
	if _, err := rand.Read(rawNonce); err != nil {
		t.Fatal(err)
	}
	controller := &previewController{t: t, listener: listener, changed: make(chan struct{}), clients: make(map[int]net.Conn), closed: make(chan struct{})}
	controller.nonce = fmt.Sprintf("%x", rawNonce)
	go controller.accept()
	t.Cleanup(func() { controller.close() })
	return controller
}

func (controller *previewController) address() string { return controller.listener.Addr().String() }

func (controller *previewController) accept() {
	defer close(controller.closed)
	for {
		connection, err := controller.listener.Accept()
		if err != nil {
			return
		}
		go controller.read(connection)
	}
}

func (controller *previewController) read(connection net.Conn) {
	defer connection.Close()
	for {
		var event controlEvent
		if err := readControlFrame(connection, &event); err != nil {
			return
		}
		if event.Nonce != controller.nonce {
			return
		}
		controller.mu.Lock()
		controller.events = append(controller.events, event)
		if event.Event == "renderer-started" {
			controller.clients[event.PID] = connection
		}
		close(controller.changed)
		controller.changed = make(chan struct{})
		controller.mu.Unlock()
	}
}

func (controller *previewController) wait(ctx context.Context, event string, count int) controlEvent {
	controller.t.Helper()
	for {
		controller.mu.Lock()
		seen := 0
		var matched controlEvent
		for _, candidate := range controller.events {
			if candidate.Event == event {
				seen++
				matched = candidate
				if seen >= count {
					controller.mu.Unlock()
					return matched
				}
			}
		}
		changed := controller.changed
		controller.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			controller.t.Fatalf("wait controller event %s #%d: %v; events=%+v", event, count, ctx.Err(), controller.snapshot())
		}
	}
}

func (controller *previewController) waitGrandchild(ctx context.Context, parent int) controlEvent {
	controller.t.Helper()
	for {
		controller.mu.Lock()
		for _, event := range controller.events {
			if event.Event == "grandchild-started" {
				if ppid, err := linuxParentPID(event.PID); err == nil && ppid == parent {
					controller.mu.Unlock()
					return event
				}
			}
		}
		changed := controller.changed
		controller.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			controller.t.Fatalf("wait grandchild of %d: %v; events=%+v", parent, ctx.Err(), controller.snapshot())
		}
	}
}

func (controller *previewController) release(pid int) error {
	controller.mu.Lock()
	connection := controller.clients[pid]
	controller.mu.Unlock()
	if connection == nil {
		return errors.New("renderer connection unavailable")
	}
	return writeControlFrame(connection, controlEvent{Event: "release", Nonce: controller.nonce})
}

func (controller *previewController) snapshot() []controlEvent {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return append([]controlEvent(nil), controller.events...)
}

func (controller *previewController) close() {
	_ = controller.listener.Close()
	controller.mu.Lock()
	for _, connection := range controller.clients {
		_ = connection.Close()
	}
	controller.mu.Unlock()
	<-controller.closed
}

type blockingPreviewFixture struct {
	*realFZFFixture
	controller *previewController
	fakeBin    string
	helper     string
}

func newBlockingPreviewFixture(t *testing.T, fzfPath string) *blockingPreviewFixture {
	t.Helper()
	base := newRealFZFFixture(t, fzfPath, "preview callback path with spaces")
	controller := newPreviewController(t)
	helper := filepath.Join(base.root, "renderer-helper")
	fakeBin := filepath.Join(base.root, "fake tools")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	buildCommand(t, repository, helper, "./integration/testhelper")
	eza := filepath.Join(fakeBin, "eza")
	ldflags := "-X=main.helperPath=" + helper + " -X=main.controller=" + controller.address() + " -X=main.nonce=" + controller.nonce
	buildCommand(t, repository, eza, "-ldflags", ldflags, "./integration/testhelper/delegate")
	return &blockingPreviewFixture{realFZFFixture: base, controller: controller, fakeBin: fakeBin, helper: helper}
}

func buildCommand(t *testing.T, directory, output string, arguments ...string) {
	t.Helper()
	args := append([]string{"build", "-o", output}, arguments...)
	command := exec.Command("go", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, combined)
	}
}

func (fixture *blockingPreviewFixture) Start(t *testing.T) terminalSession {
	path := fixture.fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	return fixture.start(t, protocol.PickerCP, []string{"PATH=" + path})
}

func (fixture *blockingPreviewFixture) setRendererMode(t *testing.T, mode string) {
	t.Helper()
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	flags := "-X=main.helperPath=" + fixture.helper + " -X=main.controller=" + fixture.controller.address() +
		" -X=main.nonce=" + fixture.controller.nonce + " -X=main.subcommand=" + mode
	buildCommand(t, repository, filepath.Join(fixture.fakeBin, "eza"), "-ldflags", flags, "./integration/testhelper/delegate")
}

type observedPreviewTree struct {
	CallbackPID, RendererPID, GrandchildPID int
	CallbackFD, RendererFD, GrandchildFD    int
	Columns, Lines                          int
}

type linuxProcessNode struct {
	pid, ppid int
	exe       string
	args      []string
}

func (fixture *blockingPreviewFixture) waitTree(t *testing.T, rendererCount int) observedPreviewTree {
	t.Helper()
	ctx := testContext(t)
	renderer := fixture.controller.wait(ctx, "renderer-started", rendererCount)
	grandchild := fixture.controller.waitGrandchild(ctx, renderer.PID)
	callback, err := linuxParentPID(renderer.PID)
	if err != nil {
		t.Fatal(err)
	}
	wantExe, err := filepath.EvalSymlinks(fixture.picker)
	if err != nil {
		t.Fatal(err)
	}
	callbackExe, err := os.Readlink("/proc/" + strconv.Itoa(callback) + "/exe")
	if err != nil || callbackExe != wantExe {
		t.Fatalf("callback executable=%q err=%v want %q", callbackExe, err, wantExe)
	}
	helperExe, err := filepath.EvalSymlinks(fixture.helper)
	if err != nil {
		t.Fatal(err)
	}
	for role, pid := range map[string]int{"renderer": renderer.PID, "grandchild": grandchild.PID} {
		executable, readErr := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		if readErr != nil || executable != helperExe {
			t.Fatalf("%s executable=%q err=%v want %q", role, executable, readErr, helperExe)
		}
	}
	fds := make([]int, 3)
	for index, pid := range []int{callback, renderer.PID, grandchild.PID} {
		fds[index], err = unix.PidfdOpen(pid, 0)
		if err != nil {
			for _, fd := range fds[:index] {
				_ = unix.Close(fd)
			}
			t.Fatalf("pidfd_open(%d): %v", pid, err)
		}
	}
	return observedPreviewTree{CallbackPID: callback, RendererPID: renderer.PID, GrandchildPID: grandchild.PID,
		CallbackFD: fds[0], RendererFD: fds[1], GrandchildFD: fds[2], Columns: renderer.Columns, Lines: renderer.Lines}
}

func (tree observedPreviewTree) close() {
	for _, fd := range []int{tree.CallbackFD, tree.RendererFD, tree.GrandchildFD} {
		_ = unix.Close(fd)
	}
}

func linuxParentPID(pid int) (int, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, errors.New("malformed proc stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 2 {
		return 0, errors.New("short proc stat")
	}
	return strconv.Atoi(fields[1])
}

func assertRealSessionProcessTree(t *testing.T, fixture *blockingPreviewFixture, term terminalSession, tree observedPreviewTree) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[int]linuxProcessNode)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		ppid, err := linuxParentPID(pid)
		if err != nil {
			continue
		}
		exe, err := os.Readlink("/proc/" + entry.Name() + "/exe")
		if err != nil {
			continue
		}
		rawArgs, _ := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		fields := strings.Split(strings.TrimSuffix(string(rawArgs), "\x00"), "\x00")
		nodes[pid] = linuxProcessNode{pid: pid, ppid: ppid, exe: exe, args: fields}
	}
	descendants := map[int]bool{term.PID(): true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid] = true
				changed = true
			}
		}
	}
	wantFZF, err := filepath.EvalSymlinks(fixture.fzf)
	if err != nil {
		t.Fatal(err)
	}
	fzfCount := 0
	for pid := range descendants {
		if pid == term.PID() {
			continue
		}
		node := nodes[pid]
		if node.exe == wantFZF {
			fzfCount++
			if node.ppid != term.PID() {
				t.Fatalf("fzf pid %d parent=%d want picker %d", pid, node.ppid, term.PID())
			}
		}
		base := strings.ToLower(filepath.Base(node.exe))
		if map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "cmd.exe": true, "powershell.exe": true}[base] {
			t.Fatalf("interpreter in real picker tree: %+v", node)
		}
		for _, argument := range node.args {
			if argument == "--listen" || strings.HasPrefix(argument, "--listen=") || strings.Contains(argument, "SHELL_PICKER_TOKEN") {
				t.Fatalf("listener or callback credential in process argv: %+v", node)
			}
		}
	}
	if fzfCount != 1 || !descendants[tree.CallbackPID] || !descendants[tree.RendererPID] || !descendants[tree.GrandchildPID] {
		t.Fatalf("process tree fzf=%d callback/renderer/grandchild=%v/%v/%v", fzfCount,
			descendants[tree.CallbackPID], descendants[tree.RendererPID], descendants[tree.GrandchildPID])
	}
}

func waitTreeExit(ctx context.Context, tree observedPreviewTree) error {
	polls := []unix.PollFd{{Fd: int32(tree.CallbackFD), Events: unix.POLLIN}, {Fd: int32(tree.RendererFD), Events: unix.POLLIN},
		{Fd: int32(tree.GrandchildFD), Events: unix.POLLIN}}
	for {
		remaining := time.Until(deadline(ctx))
		if remaining <= 0 {
			return ctx.Err()
		}
		timeout := unix.NsecToTimespec(remaining.Nanoseconds())
		if _, err := unix.Ppoll(polls, &timeout, nil); err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		all := true
		for _, poll := range polls {
			all = all && poll.Revents != 0
		}
		if all {
			return nil
		}
	}
}

func deadline(ctx context.Context) time.Time {
	value, _ := ctx.Deadline()
	return value
}

func TestRealFZFPreviewReplacementKillsWholeTree(t *testing.T) {
	fixture := newBlockingPreviewFixture(t, requireRealFZF(t))
	term := fixture.Start(t)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	first := fixture.waitTree(t, 1)
	defer first.close()
	assertRealSessionProcessTree(t, fixture, term, first)
	if events := fixture.controller.snapshot(); len(events) != 2 {
		t.Fatalf("steady preview tree events=%+v, want one renderer and one grandchild", events)
	}
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	second := fixture.waitTree(t, 2)
	defer second.close()
	if first.RendererPID == second.RendererPID || first.CallbackPID == second.CallbackPID {
		t.Fatalf("preview replacement reused process identity: first=%+v second=%+v", first, second)
	}
	if events := fixture.controller.snapshot(); len(events) > 4 {
		t.Fatalf("replacement exceeded two renderer trees: %+v", events)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := waitTreeExit(ctx, first); err != nil {
		t.Fatalf("old callback/renderer/grandchild did not exit: %v", err)
	}
	for _, event := range fixture.controller.snapshot() {
		if event.Event == "renderer-exit" && event.PID == first.RendererPID {
			t.Fatalf("killed renderer claimed normal completion: %+v", event)
		}
	}
	for _, event := range term.TraceEvents() {
		if event.Event == "preview.exit" {
			t.Fatalf("killed callback claimed finished telemetry: %+v", event)
		}
	}
	if err := fixture.controller.release(second.RendererPID); err != nil {
		t.Fatal(err)
	}
	finished := fixture.controller.wait(testContext(t), "renderer-exit", 1)
	if finished.PID != second.RendererPID {
		t.Fatalf("renderer exit=%+v want pid %d", finished, second.RendererPID)
	}
	if err := waitTreeExit(testContext(t), second); err != nil {
		t.Fatalf("released preview tree did not exit: %v", err)
	}
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRealFZFResizeUpdatesPreviewDimensions(t *testing.T) {
	fixture := newBlockingPreviewFixture(t, requireRealFZF(t))
	if err := os.Remove(filepath.Join(fixture.fakeBin, "eza")); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	failFlags := "-X=main.helperPath=" + fixture.helper + " -X=main.controller=" + fixture.controller.address() +
		" -X=main.nonce=" + fixture.controller.nonce + " -X=main.subcommand=fail"
	buildCommand(t, repository, filepath.Join(fixture.fakeBin, "eza"), "-ldflags", failFlags, "./integration/testhelper/delegate")
	ldflags := "-X=main.helperPath=" + fixture.helper + " -X=main.controller=" + fixture.controller.address() + " -X=main.nonce=" + fixture.controller.nonce
	buildCommand(t, repository, filepath.Join(fixture.fakeBin, "chafa"), "-ldflags", ldflags, "./integration/testhelper/delegate")
	for _, name := range []string{"image-a.png", "image-b.png", "image-c.png"} {
		if err := os.WriteFile(filepath.Join(fixture.cwd, name), []byte("\x89PNG\r\n\x1a\nfixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	term := fixture.Start(t)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	if err := term.Send([]byte("image")); err != nil {
		t.Fatal(err)
	}
	first := fixture.waitTree(t, 1)
	defer first.close()
	if err := term.Resize(101, 37); err != nil {
		t.Fatal(err)
	}
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	second := fixture.waitTree(t, 2)
	defer second.close()
	if err := term.Resize(83, 29); err != nil {
		t.Fatal(err)
	}
	if err := term.Send([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatal(err)
	}
	third := fixture.waitTree(t, 3)
	defer third.close()
	if second.Columns != 46 || second.Lines != 35 || third.Columns != 37 || third.Lines != 27 {
		t.Fatalf("preview dimensions initial=%dx%d resize1=%dx%d want 46x35 resize2=%dx%d want 37x27",
			first.Columns, first.Lines, second.Columns, second.Lines, third.Columns, third.Lines)
	}
	if err := fixture.controller.release(third.RendererPID); err != nil {
		t.Fatal(err)
	}
	finished := fixture.controller.wait(testContext(t), "renderer-exit", 1)
	if finished.PID != third.RendererPID {
		t.Fatalf("renderer exit=%+v want pid %d", finished, third.RendererPID)
	}
	for _, tree := range []observedPreviewTree{first, second, third} {
		if err := waitTreeExit(testContext(t), tree); err != nil {
			t.Fatalf("preview tree %+v did not exit: %v", tree, err)
		}
	}
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRealFZFPreviewTerminalFailuresKillWholeTree(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  string
		bound time.Duration
	}{
		{name: "deadline", mode: "renderer", bound: 15 * time.Second},
		{name: "output-overflow", mode: "overflow-renderer", bound: 5 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBlockingPreviewFixture(t, requireRealFZF(t))
			fixture.setRendererMode(t, test.mode)
			term := fixture.Start(t)
			defer term.Close()
			term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
			term.AssertProcessTopology(t)
			tree := fixture.waitTree(t, 1)
			defer tree.close()
			ctx, cancel := context.WithTimeout(context.Background(), test.bound)
			defer cancel()
			if err := waitTreeExit(ctx, tree); err != nil {
				t.Fatalf("terminal preview tree survived: %v", err)
			}
			controllerEvents := fixture.controller.snapshot()
			if len(controllerEvents) != 2 || controllerEvents[0].Event != "renderer-started" ||
				controllerEvents[1].Event != "grandchild-started" {
				t.Fatalf("preview child budget events=%+v, want one sequential renderer and one grandchild tree", controllerEvents)
			}
			for _, event := range controllerEvents {
				if event.Event == "renderer-exit" {
					t.Fatalf("terminal renderer claimed normal completion: %+v", event)
				}
			}
			dispatches := 0
			for _, event := range term.TraceEvents() {
				if event.Event == "preview.dispatch" {
					dispatches++
					if event.Renderer != "eza" {
						t.Fatalf("native fallback or unexpected renderer: %+v", event)
					}
				}
				if event.Event == "preview.exit" {
					t.Fatalf("terminal callback claimed finished telemetry: %+v", event)
				}
			}
			if dispatches != 1 {
				t.Fatalf("preview dispatches=%d want 1; events=%+v", dispatches, term.TraceEvents())
			}
			sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
			if err := term.Send([]byte("q")); err != nil {
				t.Fatal(err)
			}
			if err := term.Wait(testContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
