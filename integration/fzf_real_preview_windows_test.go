//go:build windows

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"golang.org/x/sys/windows"
)

type previewController struct {
	t           *testing.T
	name, nonce string
	security    *windows.SecurityAttributes
	mu          sync.Mutex
	events      []controlEvent
	changed     chan struct{}
	clients     map[int]*os.File
	listening   windows.Handle
	closing     bool
	ready       chan struct{}
	closed      chan struct{}
	readers     sync.WaitGroup
}

func newPreviewController(t *testing.T) *previewController {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		t.Fatalf("controller security descriptor: %v", err)
	}
	c := &previewController{t: t, name: `\\.\pipe\shell-picker-controller-` + hex.EncodeToString(raw[:16]),
		nonce: hex.EncodeToString(raw[16:]), security: security, changed: make(chan struct{}), clients: make(map[int]*os.File),
		ready: make(chan struct{}), closed: make(chan struct{})}
	go c.accept()
	select {
	case <-c.ready:
	case <-testContext(t).Done():
		t.Fatal("controller server did not become ready")
	}
	t.Cleanup(c.close)
	return c
}

func (c *previewController) address() string { return c.name }

func (c *previewController) accept() {
	defer close(c.closed)
	first := true
	signalReady := c.ready
	for {
		wide, err := windows.UTF16PtrFromString(c.name)
		if err != nil {
			c.fail(err)
			return
		}
		flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
		if first {
			flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
		}
		handle, err := windows.CreateNamedPipe(wide, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
			windows.PIPE_UNLIMITED_INSTANCES, 4096, 4096, 0, c.security)
		if err != nil {
			c.fail(err)
			return
		}
		c.mu.Lock()
		if c.closing {
			c.mu.Unlock()
			_ = windows.CloseHandle(handle)
			return
		}
		c.listening = handle
		c.mu.Unlock()
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			_ = windows.CloseHandle(handle)
			c.fail(err)
			return
		}
		overlapped := windows.Overlapped{HEvent: event}
		err = windows.ConnectNamedPipe(handle, &overlapped)
		close(signalReady)
		first = false
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			var transferred uint32
			err = windows.GetOverlappedResult(handle, &overlapped, &transferred, true)
		}
		_ = windows.CloseHandle(event)
		c.mu.Lock()
		c.listening = 0
		closing := c.closing
		c.mu.Unlock()
		if closing {
			_ = windows.CloseHandle(handle)
			return
		}
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			_ = windows.CloseHandle(handle)
			c.fail(err)
			return
		}
		var clientPID uint32
		if err := windows.GetNamedPipeClientProcessId(handle, &clientPID); err != nil {
			_ = windows.CloseHandle(handle)
			c.fail(err)
			return
		}
		connection := os.NewFile(uintptr(handle), c.name)
		c.mu.Lock()
		c.clients[int(clientPID)] = connection
		c.mu.Unlock()
		nextReady := make(chan struct{})
		c.readers.Add(1)
		go c.read(connection, int(clientPID), nextReady)
		signalReady = nextReady
	}
}

func (c *previewController) read(connection *os.File, clientPID int, nextReady <-chan struct{}) {
	defer c.readers.Done()
	defer func() {
		c.mu.Lock()
		if c.clients[clientPID] == connection {
			delete(c.clients, clientPID)
		}
		c.mu.Unlock()
		_ = connection.Close()
	}()
	for {
		var event controlEvent
		if err := readControlFrame(connection, &event); err != nil {
			return
		}
		if event.Nonce != c.nonce || event.PID != clientPID {
			c.fail(fmt.Errorf("controller identity mismatch pid=%d event=%+v", clientPID, event))
			return
		}
		if event.Event == "renderer-started" {
			select {
			case <-nextReady:
			case <-c.closed:
				return
			}
			if err := writeControlFrame(connection, controlEvent{Event: "controller-ready", Nonce: c.nonce}); err != nil {
				c.fail(err)
				return
			}
		}
		c.mu.Lock()
		c.events = append(c.events, event)
		close(c.changed)
		c.changed = make(chan struct{})
		c.mu.Unlock()
	}
}

func (c *previewController) fail(err error) {
	c.mu.Lock()
	c.events = append(c.events, controlEvent{Event: "controller-error", PID: -1, Nonce: err.Error()})
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
}

func (c *previewController) wait(ctx context.Context, event string, count int) controlEvent {
	c.t.Helper()
	for {
		c.mu.Lock()
		seen := 0
		for _, candidate := range c.events {
			if candidate.Event == "controller-error" {
				c.mu.Unlock()
				c.t.Fatalf("controller: %s", candidate.Nonce)
			}
			if candidate.Event == event {
				seen++
				if seen >= count {
					c.mu.Unlock()
					return candidate
				}
			}
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			c.t.Fatalf("wait %s #%d: %v events=%+v", event, count, ctx.Err(), c.snapshot())
		}
	}
}

func (c *previewController) release(pid int) error {
	c.mu.Lock()
	connection := c.clients[pid]
	c.mu.Unlock()
	if connection == nil {
		return errors.New("renderer connection unavailable")
	}
	return writeControlFrame(connection, controlEvent{Event: "release", Nonce: c.nonce})
}

func (c *previewController) snapshot() []controlEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]controlEvent(nil), c.events...)
}

func (c *previewController) close() {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		<-c.closed
		return
	}
	c.closing = true
	listening := c.listening
	for _, client := range c.clients {
		_ = windows.CancelIoEx(windows.Handle(client.Fd()), nil)
	}
	c.mu.Unlock()
	if listening != 0 {
		_ = windows.CancelIoEx(listening, nil)
	}
	<-c.closed
	c.readers.Wait()
}

type blockingPreviewFixture struct {
	*realFZFFixture
	controller      *previewController
	fakeBin, helper string
}

func newBlockingPreviewFixture(t *testing.T, fzfPath string) *blockingPreviewFixture {
	t.Helper()
	base := newRealFZFFixture(t, fzfPath, "preview callback path with spaces")
	controller := newPreviewController(t)
	helper := filepath.Join(base.root, "renderer-helper.exe")
	fakeBin := filepath.Join(base.root, "fake tools")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	buildWindowsCommand(t, repository, helper, "./integration/testhelper")
	buildWindowsCommand(t, repository, filepath.Join(fakeBin, "eza.exe"), "-ldflags",
		"-X=main.helperPath="+helper+" -X=main.controller="+controller.address()+" -X=main.nonce="+controller.nonce,
		"./integration/testhelper/delegate")
	return &blockingPreviewFixture{realFZFFixture: base, controller: controller, fakeBin: fakeBin, helper: helper}
}

func buildWindowsCommand(t *testing.T, directory, output string, arguments ...string) {
	t.Helper()
	args := append([]string{"build", "-o", output}, arguments...)
	command := exec.Command("go", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, combined)
	}
}

func (f *blockingPreviewFixture) Start(t *testing.T) terminalSession {
	return f.start(t, protocol.PickerCP, []string{"PATH=" + f.fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")})
}

func (f *blockingPreviewFixture) setRendererMode(t *testing.T, mode string) {
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	buildWindowsCommand(t, repository, filepath.Join(f.fakeBin, "eza.exe"), "-ldflags",
		"-X=main.helperPath="+f.helper+" -X=main.controller="+f.controller.address()+" -X=main.nonce="+f.controller.nonce+" -X=main.subcommand="+mode,
		"./integration/testhelper/delegate")
}

type observedPreviewTree struct {
	CallbackPID, RendererPID, GrandchildPID          int
	CallbackHandle, RendererHandle, GrandchildHandle windows.Handle
	Columns, Lines                                   int
}

func (f *blockingPreviewFixture) waitTree(t *testing.T, count int) observedPreviewTree {
	t.Helper()
	renderer := f.controller.wait(testContext(t), "renderer-started", count)
	grandchild := f.controller.wait(testContext(t), "grandchild-started", count)
	nodes, err := snapshotWindowsProcesses(false)
	if err != nil {
		t.Fatal(err)
	}
	rendererNode, ok := nodes[uint32(renderer.PID)]
	if !ok || rendererNode.queryErr != nil {
		t.Fatalf("renderer %d Toolhelp/query result=%+v", renderer.PID, rendererNode)
	}
	grandchildNode, ok := nodes[uint32(grandchild.PID)]
	if !ok || grandchildNode.queryErr != nil || grandchildNode.ppid != uint32(renderer.PID) {
		t.Fatalf("grandchild topology=%+v", grandchildNode)
	}
	callback := int(rendererNode.ppid)
	callbackNode, ok := nodes[uint32(callback)]
	if !ok || callbackNode.queryErr != nil || !stringsEqualPath(callbackNode.exe, f.picker) {
		t.Fatalf("callback=%+v want %q", callbackNode, f.picker)
	}
	if !stringsEqualPath(filepath.Dir(rendererNode.exe), f.fakeBin) || !stringsEqualPath(grandchildNode.exe, f.helper) {
		t.Fatalf("delegate/helper identities renderer=%q grandchild=%q want dir/helper=%q/%q", rendererNode.exe, grandchildNode.exe, f.fakeBin, f.helper)
	}
	switch strings.ToLower(filepath.Base(rendererNode.exe)) {
	case "eza.exe", "chafa.exe":
	default:
		t.Fatalf("unexpected renderer helper identity %q", rendererNode.exe)
	}
	handles := make([]windows.Handle, 3)
	for i, pid := range []int{callback, renderer.PID, grandchild.PID} {
		handles[i], err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			for _, h := range handles[:i] {
				_ = windows.CloseHandle(h)
			}
			t.Fatalf("OpenProcess(%d): %v", pid, err)
		}
	}
	return observedPreviewTree{CallbackPID: callback, RendererPID: renderer.PID, GrandchildPID: grandchild.PID,
		CallbackHandle: handles[0], RendererHandle: handles[1], GrandchildHandle: handles[2], Columns: renderer.Columns, Lines: renderer.Lines}
}

func stringsEqualPath(left, right string) bool {
	leftAbs, _ := filepath.Abs(left)
	rightAbs, _ := filepath.Abs(right)
	return equalFold(leftAbs, rightAbs)
}
func equalFold(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func (tree observedPreviewTree) close() {
	for _, h := range []windows.Handle{tree.CallbackHandle, tree.RendererHandle, tree.GrandchildHandle} {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
	}
}

func waitTreeExit(ctx context.Context, tree observedPreviewTree) error {
	for _, handle := range []windows.Handle{tree.CallbackHandle, tree.RendererHandle, tree.GrandchildHandle} {
		deadline, ok := ctx.Deadline()
		if !ok {
			return errors.New("tree wait requires deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return ctx.Err()
		}
		status, err := windows.WaitForSingleObject(handle, uint32(remaining/time.Millisecond))
		if err != nil {
			return err
		}
		if status != windows.WAIT_OBJECT_0 {
			return context.DeadlineExceeded
		}
	}
	return nil
}

func TestRealFZFPreviewReplacementKillsWholeTree(t *testing.T) {
	f := newBlockingPreviewFixture(t, requireRealFZF(t))
	term := f.Start(t)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	first := f.waitTree(t, 1)
	defer first.close()
	if len(f.controller.snapshot()) != 2 {
		t.Fatalf("steady tree events=%+v", f.controller.snapshot())
	}
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	second := f.waitTree(t, 2)
	defer second.close()
	if len(f.controller.snapshot()) > 4 {
		t.Fatalf("replacement tree budget exceeded: %+v", f.controller.snapshot())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := waitTreeExit(ctx, first); err != nil {
		t.Fatalf("old complete tree survived: %v", err)
	}
	for _, event := range f.controller.snapshot() {
		if event.Event == "renderer-exit" && event.PID == first.RendererPID {
			t.Fatalf("killed renderer claimed completion")
		}
	}
	assertPreviewTraceCount(t, term.TraceEvents(), "preview.dispatch", "eza", "ok", 2)
	if err := f.controller.release(second.RendererPID); err != nil {
		t.Fatal(err)
	}
	if event := f.controller.wait(testContext(t), "renderer-exit", 1); event.PID != second.RendererPID {
		t.Fatalf("exit=%+v", event)
	}
	if err := waitTreeExit(testContext(t), second); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: 1})
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	assertFinishedTrace(t, term.TraceEvents(), "eza", 1)
}

func TestRealFZFResizeUpdatesPreviewDimensions(t *testing.T) {
	f := newBlockingPreviewFixture(t, requireRealFZF(t))
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	buildWindowsCommand(t, repository, filepath.Join(f.fakeBin, "chafa.exe"), "-ldflags",
		"-X=main.helperPath="+f.helper+" -X=main.controller="+f.controller.address()+" -X=main.nonce="+f.controller.nonce, "./integration/testhelper/delegate")
	for _, name := range []string{"image-a.png", "image-b.png", "image-c.png"} {
		if err := os.WriteFile(filepath.Join(f.cwd, name), []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	term := f.Start(t)
	defer term.Close()
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.AssertProcessTopology(t)
	initial := f.waitTree(t, 1)
	defer initial.close()
	if err := term.Send([]byte("image")); err != nil {
		t.Fatal(err)
	}
	first := f.waitTree(t, 2)
	defer first.close()
	if err := waitTreeExit(testContext(t), initial); err != nil {
		t.Fatalf("initial directory preview tree did not exit: %v", err)
	}
	if err := term.Resize(101, 37); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send(keyDown); err != nil {
		t.Fatal(err)
	}
	second := f.waitTree(t, 3)
	defer second.close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Renderer: "chafa", Operation: "ok", Count: 2})
	if err := term.Resize(83, 29); err != nil {
		t.Fatal(err)
	}
	sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 2})
	if err := term.Send([]byte{0x1b, '[', 'A'}); err != nil {
		t.Fatal(err)
	}
	third := f.waitTree(t, 4)
	defer third.close()
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Renderer: "chafa", Operation: "ok", Count: 3})
	if second.Columns != 46 || second.Lines != 35 || third.Columns != 37 || third.Lines != 27 {
		t.Fatalf("dimensions first=%dx%d second=%dx%d third=%dx%d", first.Columns, first.Lines, second.Columns, second.Lines, third.Columns, third.Lines)
	}
	assertPreviewTraceCount(t, term.TraceEvents(), "preview.dispatch", "chafa", "ok", 3)
	if err := f.controller.release(third.RendererPID); err != nil {
		t.Fatal(err)
	}
	f.controller.wait(testContext(t), "renderer-exit", 1)
	term.WaitBarrier(testContext(t), barrier{Event: "preview.finished", Operation: "ok", Renderer: "chafa", Count: 1})
	for _, tree := range []observedPreviewTree{initial, first, second, third} {
		if err := waitTreeExit(testContext(t), tree); err != nil {
			t.Fatal(err)
		}
	}
	if err := term.Send([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := term.Wait(testContext(t)); err != nil {
		t.Fatal(err)
	}
	term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
	assertFinishedTrace(t, term.TraceEvents(), "chafa", 1)
}

func TestRealFZFPreviewTerminalFailuresKillWholeTree(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		bound      time.Duration
	}{{"deadline", "renderer", 15 * time.Second}, {"output-overflow", "overflow-renderer", 5 * time.Second}} {
		t.Run(test.name, func(t *testing.T) {
			f := newBlockingPreviewFixture(t, requireRealFZF(t))
			f.setRendererMode(t, test.mode)
			term := f.Start(t)
			defer term.Close()
			term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
			term.AssertProcessTopology(t)
			tree := f.waitTree(t, 1)
			defer tree.close()
			ctx, cancel := context.WithTimeout(context.Background(), test.bound)
			defer cancel()
			if err := waitTreeExit(ctx, tree); err != nil {
				t.Fatal(err)
			}
			events := f.controller.snapshot()
			if len(events) != 2 {
				t.Fatalf("tree budget=%+v", events)
			}
			for _, event := range events {
				if event.Event == "renderer-exit" {
					t.Fatalf("terminal renderer claimed completion")
				}
			}
			sendAndWait(t, term, keyEsc, barrier{Event: "callback.event", Operation: "es", Count: 1})
			if err := term.Send([]byte("q")); err != nil {
				t.Fatal(err)
			}
			if err := term.Wait(testContext(t)); err != nil {
				t.Fatal(err)
			}
			term.WaitBarrier(testContext(t), barrier{Event: "session.close", Operation: "aborted", Count: 1})
			assertPreviewTraceCount(t, term.TraceEvents(), "preview.dispatch", "eza", "ok", 1)
			assertFinishedTrace(t, term.TraceEvents(), "eza", 0)
		})
	}
}
