//go:build windows

package app

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestClassifyWindowsTracePath(t *testing.T) {
	for _, test := range []struct {
		path string
		pipe bool
	}{
		{path: `\\.\pipe\trace`, pipe: true},
		{path: `\\.\PIPE\trace`, pipe: true},
		{path: `C:\trace.jsonl`, pipe: false},
		{path: `\\server\share\trace`, pipe: false},
		{path: `\\?\pipe\trace`, pipe: false},
	} {
		if got := isCanonicalNamedPipePath(test.path); got != test.pipe {
			t.Errorf("isCanonicalNamedPipePath(%q)=%v want %v", test.path, got, test.pipe)
		}
	}
}

func TestOpenWindowsTraceSinkUsesPipeAndFileDispositions(t *testing.T) {
	tests := []struct {
		name, path   string
		wantAccess   uint32
		wantCreation uint32
		wantFlags    uint32
	}{
		{name: "named pipe", path: `\\.\pipe\random`, wantAccess: windows.GENERIC_WRITE,
			wantCreation: windows.OPEN_EXISTING, wantFlags: windows.FILE_ATTRIBUTE_NORMAL},
		{name: "ordinary file", path: `C:\trace.jsonl`, wantAccess: windows.GENERIC_WRITE | windows.WRITE_DAC,
			wantCreation: windows.OPEN_ALWAYS, wantFlags: windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var call windowsTraceCreateCall
			ops := windowsTraceOps{
				createFile:   func(got windowsTraceCreateCall) (windows.Handle, error) { call = got; return 42, nil },
				validateFile: func(windows.Handle) error { return nil },
				truncateFile: func(windows.Handle) error { return nil },
				closeHandle:  func(windows.Handle) error { return nil },
			}
			handle, err := openWindowsTraceHandle(test.path, ops)
			if err != nil || handle != 42 {
				t.Fatalf("handle=%v err=%v", handle, err)
			}
			if call.Access != test.wantAccess || call.Creation != test.wantCreation || call.Flags != test.wantFlags {
				t.Fatalf("call=%+v", call)
			}
		})
	}
}

func TestOpenWindowsTraceSinkValidatesBeforeTruncatingAndClosesOnFailure(t *testing.T) {
	validationErr := errors.New("reparse point")
	var truncated, closed bool
	ops := windowsTraceOps{
		createFile:   func(windowsTraceCreateCall) (windows.Handle, error) { return 42, nil },
		validateFile: func(windows.Handle) error { return validationErr },
		truncateFile: func(windows.Handle) error { truncated = true; return nil },
		closeHandle:  func(windows.Handle) error { closed = true; return nil },
	}
	if _, err := openWindowsTraceHandle(`C:\trace`, ops); !errors.Is(err, validationErr) {
		t.Fatalf("error=%v", err)
	}
	if truncated || !closed {
		t.Fatalf("truncated=%v closed=%v", truncated, closed)
	}
}
