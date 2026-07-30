//go:build windows

package integration

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

type windowsLifecycleRecorder struct {
	mu            sync.Mutex
	operations    []string
	closed        map[windows.Handle]int
	terminateHook func()
	cancelHook    func(windows.Handle)
}

func (recorder *windowsLifecycleRecorder) add(operation string) {
	recorder.mu.Lock()
	recorder.operations = append(recorder.operations, operation)
	recorder.mu.Unlock()
}

func (recorder *windowsLifecycleRecorder) ops() windowsTerminalOps {
	if recorder.closed == nil {
		recorder.closed = make(map[windows.Handle]int)
	}
	return windowsTerminalOps{
		closeHandle: func(handle windows.Handle) error {
			recorder.mu.Lock()
			recorder.closed[handle]++
			recorder.operations = append(recorder.operations, "close")
			recorder.mu.Unlock()
			return nil
		},
		terminateProcess: func(windows.Handle, uint32) error {
			recorder.add("terminate")
			if recorder.terminateHook != nil {
				recorder.terminateHook()
			}
			return nil
		},
		cancelIO: func(handle windows.Handle, _ *windows.Overlapped) error {
			recorder.add("cancel")
			if recorder.cancelHook != nil {
				recorder.cancelHook(handle)
			}
			return nil
		},
		closePseudoConsole:  func(windows.Handle) { recorder.add("console") },
		resizePseudoConsole: func(windows.Handle, windows.Coord) error { recorder.add("resize"); return nil },
		writeFile:           func(windows.Handle, []byte, *uint32, *windows.Overlapped) error { recorder.add("write"); return nil },
		waitForSingleObject: func(windows.Handle, uint32) (uint32, error) { return windows.WAIT_OBJECT_0, nil },
		getExitCodeProcess:  func(windows.Handle, *uint32) error { return nil },
	}
}

func TestWindowsTerminalSessionLifecycleUsesActualStateAndOps(t *testing.T) {
	recorder := &windowsLifecycleRecorder{}
	ops := recorder.ops()
	drainDone, traceDone, waitDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	recorder.terminateHook = func() { close(waitDone) }
	recorder.cancelHook = func(handle windows.Handle) {
		switch handle {
		case 3:
			_ = ops.closeHandle(handle)
			close(drainDone)
		case 4:
			_ = ops.closeHandle(handle)
			close(traceDone)
		}
	}
	session := &windowsTerminalSession{ops: ops, console: 1, input: 2, output: 3, trace: 4, process: 5,
		outputStarted: true, traceStarted: true, waitStarted: true,
		drainDone: drainDone, traceDone: traceDone, waitDone: waitDone, changed: make(chan struct{})}

	var callers sync.WaitGroup
	for range 16 {
		callers.Add(2)
		go func() {
			defer callers.Done()
			if err := session.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
		go func() {
			defer callers.Done()
			if err := session.CloseInput(); err != nil {
				t.Errorf("CloseInput: %v", err)
			}
		}()
	}
	callers.Wait()
	for _, handle := range []windows.Handle{2, 3, 4, 5} {
		if recorder.closed[handle] != 1 {
			t.Errorf("handle %d closes=%d", handle, recorder.closed[handle])
		}
	}
	recorder.mu.Lock()
	operations := append([]string(nil), recorder.operations...)
	recorder.mu.Unlock()
	if len(operations) == 0 || operations[len(operations)-1] != "close" {
		t.Fatalf("cleanup did not join before final process close: %v", operations)
	}
	if err := session.Send([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Send after Close=%v", err)
	}
	if err := session.Resize(80, 24); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Resize after Close=%v", err)
	}
}

func TestWindowsTerminalSessionWaitReplaysStoredResult(t *testing.T) {
	recorder := &windowsLifecycleRecorder{}
	ops := recorder.ops()
	want := errors.New("wait failed")
	ops.getExitCodeProcess = func(windows.Handle, *uint32) error { return want }
	drainDone, traceDone := make(chan struct{}), make(chan struct{})
	close(drainDone)
	close(traceDone)
	session := &windowsTerminalSession{ops: ops, console: 1, process: 5, waitStarted: true,
		waitDone: make(chan struct{}), drainDone: drainDone, traceDone: traceDone, changed: make(chan struct{})}
	go session.waitProcess(6)
	var callers sync.WaitGroup
	for range 16 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if err := session.Wait(context.Background()); !errors.Is(err, want) {
				t.Errorf("Wait=%v", err)
			}
		}()
	}
	callers.Wait()
	if err := session.Wait(context.Background()); !errors.Is(err, want) {
		t.Fatalf("repeated Wait=%v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	for _, handle := range []windows.Handle{5, 6} {
		if recorder.closed[handle] != 1 {
			t.Errorf("process handle %d closes=%d", handle, recorder.closed[handle])
		}
	}
}

func TestWindowsTerminalResourceTraceFailureClosesEveryCreatedHandle(t *testing.T) {
	recorder := &windowsLifecycleRecorder{}
	want := errors.New("trace pipe failed")
	factory := windowsTerminalFactory{
		ops:                 recorder.ops(),
		createInputPipe:     func() (windows.Handle, windows.Handle, error) { return 10, 11, nil },
		createOutputPipe:    func() (windows.Handle, windows.Handle, error) { return 12, 13, nil },
		createPseudoConsole: func(windows.Coord, windows.Handle, windows.Handle) (windows.Handle, error) { return 14, nil },
		createTracePipe:     func() (string, windows.Handle, error) { return "", 0, want },
	}
	if _, _, err := createWindowsTerminalResources(terminalConfig{Columns: 80, Lines: 24}, factory); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	for _, handle := range []windows.Handle{10, 11, 12, 13} {
		if recorder.closed[handle] != 1 {
			t.Errorf("handle %d closes=%d", handle, recorder.closed[handle])
		}
	}
	recorder.mu.Lock()
	operations := append([]string(nil), recorder.operations...)
	recorder.mu.Unlock()
	consoleCloses := 0
	for _, operation := range operations {
		if operation == "console" {
			consoleCloses++
		}
	}
	if consoleCloses != 1 {
		t.Fatalf("pseudoconsole closes=%d operations=%v", consoleCloses, operations)
	}
}
