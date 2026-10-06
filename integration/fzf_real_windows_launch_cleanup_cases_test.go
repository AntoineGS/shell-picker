//go:build windows

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/windows"
)

var conPTYBootstrapBinary struct {
	once sync.Once
	path string
	err  error
}

func requireConPTYBootstrap(t *testing.T) string {
	t.Helper()
	conPTYBootstrapBinary.once.Do(func() {
		repository, err := filepath.Abs("..")
		if err != nil {
			conPTYBootstrapBinary.err = err
			return
		}
		root, err := os.MkdirTemp("", "shell-picker-conpty-bootstrap-")
		if err != nil {
			conPTYBootstrapBinary.err = err
			return
		}
		conPTYBootstrapBinary.path = filepath.Join(root, binaryName("conpty-bootstrap"))
		command := exec.Command("go", "build", "-o", conPTYBootstrapBinary.path, "./integration/testhelper/conpty-bootstrap")
		command.Dir = repository
		command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
		if output, buildErr := command.CombinedOutput(); buildErr != nil {
			conPTYBootstrapBinary.err = fmt.Errorf("build ConPTY bootstrap: %w\n%s", buildErr, output)
		}
	})
	if conPTYBootstrapBinary.err != nil {
		t.Fatal(conPTYBootstrapBinary.err)
	}
	return conPTYBootstrapBinary.path
}

func TestWindowsCloseProcessInformationRetainsFailedHandleForRetry(t *testing.T) {
	for _, test := range []struct {
		name        string
		failed      windows.Handle
		wantProcess windows.Handle
		wantThread  windows.Handle
	}{
		{name: "process", failed: 5, wantProcess: 5},
		{name: "thread", failed: 6, wantThread: 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &windowsLifecycleRecorder{}
			ops := recorder.ops()
			closeHandle := ops.closeHandle
			failed := true
			want := errors.New("close process information handle")
			ops.closeHandle = func(handle windows.Handle) error {
				if err := closeHandle(handle); err != nil {
					return err
				}
				if handle == test.failed && failed {
					failed = false
					return want
				}
				return nil
			}
			session := &windowsTerminalSession{ops: ops}
			information := windows.ProcessInformation{Process: 5, Thread: 6}

			if err := closeWindowsProcessInformation(session, &information); !errors.Is(err, want) {
				t.Fatalf("first closeWindowsProcessInformation=%v, want %v", err, want)
			}
			if information.Process != test.wantProcess || information.Thread != test.wantThread {
				t.Fatalf("handles after failed close=%d,%d, want %d,%d", information.Process, information.Thread, test.wantProcess, test.wantThread)
			}
			if err := closeWindowsProcessInformation(session, &information); err != nil {
				t.Fatalf("retry closeWindowsProcessInformation=%v", err)
			}
			if information.Process != 0 || information.Thread != 0 {
				t.Fatalf("handles retained after retry: process=%d thread=%d", information.Process, information.Thread)
			}
			for _, handle := range []windows.Handle{5, 6} {
				wantCloses := 1
				if handle == test.failed {
					wantCloses = 2
				}
				if recorder.closed[handle] != wantCloses {
					t.Errorf("handle %d closes=%d, want %d", handle, recorder.closed[handle], wantCloses)
				}
			}
		})
	}
}

func TestWindowsActualLaunchRetainsProcessInformationHandleAfterCloseFailure(t *testing.T) {
	for _, test := range []struct {
		name          string
		failed        windows.Handle
		triggerLaunch bool
		want          func(windows.ProcessInformation) windows.Handle
	}{
		{name: "process", failed: 5, triggerLaunch: true, want: func(information windows.ProcessInformation) windows.Handle { return information.Process }},
		{name: "thread", failed: 6, want: func(information windows.ProcessInformation) windows.Handle { return information.Thread }},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &windowsLifecycleRecorder{}
			ops := recorder.ops()
			closeHandle := ops.closeHandle
			failed := true
			closeLaunchHandle := true
			want := errors.New("injected launch close failure")
			ops.closeHandle = func(handle windows.Handle) error {
				if test.triggerLaunch && handle == 8 && closeLaunchHandle {
					closeLaunchHandle = false
					return want
				}
				if handle == test.failed && failed {
					failed = false
					return want
				}
				return closeHandle(handle)
			}
			session := &windowsTerminalSession{ops: ops, resultWrite: 8, standardInput: 9}

			err := launchWindowsProcess(session, func(information *windows.ProcessInformation) error {
				*information = windows.ProcessInformation{Process: 5, Thread: 6, ProcessId: 5}
				return nil
			})
			if !errors.Is(err, want) {
				t.Fatalf("launchWindowsProcess() error = %v, want injected close failure", err)
			}
			if got := test.want(session.launchInformation); got != test.failed {
				t.Fatalf("launch information retained handle = %d, want %d", got, test.failed)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("session.Close() retry = %v", err)
			}
			if session.launchInformation.Process != 0 || session.launchInformation.Thread != 0 {
				t.Fatalf("launch information after retry = %+v, want zero handles", session.launchInformation)
			}
		})
	}
}

func TestWindowsProductionRootDiscoveryFailureClosesOwnedWaitHandle(t *testing.T) {
	recorder := &windowsLifecycleRecorder{}
	session := &windowsTerminalSession{
		ops:        recorder.ops(),
		pid:        5,
		process:    5,
		waitHandle: 7,
		recorder:   newDescendantRecorder(nil),
	}
	want := errors.New("discover production root")
	err := finalizeWindowsTerminalLaunch(session, terminalConfig{ProductionRootPath: `C:\pwsh.exe`}, context.Background(), windowsProductionDiscoveryDeps{
		snapshot: func() (map[uint32]windowsProcessNode, error) { return nil, want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("finalizeWindowsTerminalLaunch() error = %v, want %v", err, want)
	}
	if session.waitHandle != 7 {
		t.Fatalf("wait handle after discovery failure = %d, want 7", session.waitHandle)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() = %v", err)
	}
	if session.waitHandle != 0 {
		t.Fatalf("wait handle after session.Close() = %d, want zero", session.waitHandle)
	}
	if recorder.closed[7] != 1 {
		t.Fatalf("wait handle closes = %d, want 1", recorder.closed[7])
	}
}

func TestWindowsLaunchCleanupClosesProcessInformationHandlesAfterOwnedHandleFailure(t *testing.T) {
	for _, test := range []struct {
		name              string
		failed            windows.Handle
		failedName        string
		wantResultWrite   windows.Handle
		wantStandardInput windows.Handle
	}{
		{name: "result write", failed: 8, failedName: "result write", wantResultWrite: 8},
		{name: "standard input", failed: 9, failedName: "standard input", wantStandardInput: 9},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &windowsLifecycleRecorder{}
			want := errors.New("close " + test.failedName)
			ops := recorder.ops()
			closeHandle := ops.closeHandle
			failed := true
			ops.closeHandle = func(handle windows.Handle) error {
				if err := closeHandle(handle); err != nil {
					return err
				}
				if handle == test.failed && failed {
					failed = false
					return want
				}
				return nil
			}
			session := &windowsTerminalSession{ops: ops, resultWrite: 8, standardInput: 9}
			information := windows.ProcessInformation{Process: 5, Thread: 6}

			if err := closeWindowsLaunchHandles(session, &information); !errors.Is(err, want) {
				t.Fatalf("closeWindowsLaunchHandles=%v, want %v", err, want)
			}
			if information.Process != 0 || information.Thread != 0 {
				t.Fatalf("process information retained: process=%d thread=%d", information.Process, information.Thread)
			}
			if session.resultWrite != test.wantResultWrite || session.standardInput != test.wantStandardInput {
				t.Fatalf("launch handles after failed close=%d,%d, want %d,%d", session.resultWrite, session.standardInput, test.wantResultWrite, test.wantStandardInput)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("retry launch-handle cleanup: %v", err)
			}
			if session.resultWrite != 0 || session.standardInput != 0 {
				t.Fatalf("launch handles retained after retry: resultWrite=%d standardInput=%d", session.resultWrite, session.standardInput)
			}
			for _, handle := range []windows.Handle{8, 9, 5, 6} {
				wantCloses := 1
				if handle == test.failed {
					wantCloses = 2
				}
				if recorder.closed[handle] != wantCloses {
					t.Errorf("handle %d closes=%d, want %d", handle, recorder.closed[handle], wantCloses)
				}
			}
		})
	}
}
