//go:build windows

package integration

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

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
