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
