//go:build windows

package integration

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"golang.org/x/sys/windows"
)

type windowsLifecycleRecorder struct {
	mu            sync.Mutex
	operations    []string
	closed        map[windows.Handle]int
	terminateHook func()
	cancelHook    func(windows.Handle)
	cancelError   func(windows.Handle) error
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
			if recorder.cancelError != nil {
				return recorder.cancelError(handle)
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
	drainDone, resultDone, traceDone, waitDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	recorder.terminateHook = func() { close(waitDone) }
	recorder.cancelHook = func(handle windows.Handle) {
		switch handle {
		case 3:
			_ = ops.closeHandle(handle)
			close(drainDone)
		case 4:
			_ = ops.closeHandle(handle)
			close(traceDone)
		case 7:
			_ = ops.closeHandle(handle)
			close(resultDone)
		}
	}
	session := &windowsTerminalSession{ops: ops, console: 1, input: 2, output: 3, result: 7, resultWrite: 8, standardInput: 9, trace: 4, process: 5,
		outputStarted: true, resultStarted: true, traceStarted: true, waitStarted: true,
		drainDone: drainDone, resultDone: resultDone, traceDone: traceDone, waitDone: waitDone, changed: make(chan struct{})}

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
	for _, handle := range []windows.Handle{2, 3, 4, 5, 7, 8, 9} {
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
	drainDone, resultDone, traceDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	close(drainDone)
	close(resultDone)
	close(traceDone)
	for _, handle := range []windows.Handle{3, 4, 7} {
		recorder.closed[handle] = 1
	}
	session := &windowsTerminalSession{ops: ops, console: 1, output: 3, result: 7, trace: 4, process: 5,
		outputStarted: true, resultStarted: true, traceStarted: true, waitStarted: true,
		waitDone: make(chan struct{}), drainDone: drainDone, resultDone: resultDone, traceDone: traceDone, changed: make(chan struct{})}
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
	for _, operation := range recorder.operations {
		if operation == "cancel" {
			t.Fatal("Close cancelled an already-completed worker")
		}
	}
	for _, handle := range []windows.Handle{5, 6} {
		if recorder.closed[handle] != 1 {
			t.Errorf("process handle %d closes=%d", handle, recorder.closed[handle])
		}
	}
}

func TestWindowsTerminalSessionCancelRaceAcceptsInvalidHandleAfterWorkerDone(t *testing.T) {
	recorder := &windowsLifecycleRecorder{}
	ops := recorder.ops()
	drainDone := make(chan struct{})
	ops.beforeCancelIO = func(handle windows.Handle) {
		if handle != 3 {
			return
		}
		_ = ops.closeHandle(handle)
		close(drainDone)
	}
	recorder.cancelError = func(windows.Handle) error { return windows.ERROR_INVALID_HANDLE }
	session := &windowsTerminalSession{ops: ops, output: 3, outputStarted: true,
		drainDone: drainDone, changed: make(chan struct{})}

	if err := session.Close(); err != nil {
		t.Fatalf("Close=%v, want benign invalid-handle race", err)
	}
	if session.output != 0 {
		t.Fatalf("output ownership=%d, want cleared after worker completion", session.output)
	}
	if recorder.closed[3] != 1 {
		t.Fatalf("output handle closes=%d, want one close", recorder.closed[3])
	}
}

func TestWindowsTerminalSessionCloseRetriesAfterWorkerTimeout(t *testing.T) {
	recorder := &windowsLifecycleRecorder{}
	ops := recorder.ops()
	drainDone := make(chan struct{})
	resultDone := make(chan struct{})
	traceDone := make(chan struct{})
	waitDone := make(chan struct{})
	session := &windowsTerminalSession{ops: ops, console: 1, input: 2, output: 3, result: 7, trace: 4, process: 5,
		outputStarted: true, resultStarted: true, traceStarted: true, waitStarted: true, cleanupTimeout: 10 * time.Millisecond,
		drainDone: drainDone, resultDone: resultDone, traceDone: traceDone, waitDone: waitDone, changed: make(chan struct{})}

	started := time.Now()
	if err := session.Close(); !errors.Is(err, process.ErrWaitDelay) {
		t.Fatalf("first Close=%v, want process.ErrWaitDelay", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("first Close took %s, want bounded cleanup", elapsed)
	}
	if session.output == 0 || session.result == 0 || session.trace == 0 || session.process == 0 {
		t.Fatalf("worker-owned handles cleared before workers joined: output=%d result=%d trace=%d process=%d", session.output, session.result, session.trace, session.process)
	}
	for _, handle := range []windows.Handle{3, 4, 5, 7} {
		if recorder.closed[handle] != 0 {
			t.Fatalf("worker-owned handle %d closed before its worker joined", handle)
		}
	}
	cancelsBeforeRelease := 0
	for _, operation := range recorder.operations {
		if operation == "cancel" {
			cancelsBeforeRelease++
		}
	}
	_ = ops.closeHandle(3)
	_ = ops.closeHandle(7)
	_ = ops.closeHandle(4)
	close(drainDone)
	close(resultDone)
	close(traceDone)
	close(waitDone)
	if err := session.Close(); err != nil {
		t.Fatalf("second Close=%v, want cleanup retry success", err)
	}
	cancelsAfterRelease := 0
	for _, operation := range recorder.operations {
		if operation == "cancel" {
			cancelsAfterRelease++
		}
	}
	if cancelsAfterRelease != cancelsBeforeRelease {
		t.Fatalf("second Close cancelled completed workers: before=%d after=%d", cancelsBeforeRelease, cancelsAfterRelease)
	}
	if session.console != 0 || session.input != 0 || session.output != 0 || session.result != 0 ||
		session.resultWrite != 0 || session.standardInput != 0 || session.trace != 0 || session.process != 0 {
		t.Fatalf("second Close retained ownership: console=%d input=%d output=%d result=%d resultWrite=%d standardInput=%d trace=%d process=%d",
			session.console, session.input, session.output, session.result, session.resultWrite, session.standardInput, session.trace, session.process)
	}
	for _, handle := range []windows.Handle{2, 3, 4, 5, 7} {
		if recorder.closed[handle] != 1 {
			t.Fatalf("handle %d closes=%d, want one close", handle, recorder.closed[handle])
		}
	}
	for _, done := range []<-chan struct{}{waitDone, drainDone, resultDone, traceDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("worker did not finish after cleanup retry")
		}
	}
	for _, handle := range []windows.Handle{3, 4, 5, 7} {
		if recorder.closed[handle] != 1 {
			t.Fatalf("worker-owned handle %d closes=%d, want one close", handle, recorder.closed[handle])
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
		createResultPipe:    func() (windows.Handle, windows.Handle, error) { return 14, 15, nil },
		createPseudoConsole: func(windows.Coord, windows.Handle, windows.Handle) (windows.Handle, error) { return 16, nil },
		createTracePipe:     func() (string, windows.Handle, error) { return "", 0, want },
	}
	if _, _, err := createWindowsTerminalResources(terminalConfig{Columns: 80, Lines: 24}, factory); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	for _, handle := range []windows.Handle{10, 11, 12, 13, 14, 15} {
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
