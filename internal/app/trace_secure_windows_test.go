//go:build windows

package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
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
		wantShare    uint32
		wantCreation uint32
		wantFlags    uint32
	}{
		{name: "named pipe", path: `\\.\pipe\random`, wantAccess: windows.GENERIC_WRITE, wantShare: 0,
			wantCreation: windows.OPEN_EXISTING, wantFlags: windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_WRITE_THROUGH},
		{name: "ordinary file", path: `C:\trace.jsonl`, wantAccess: windows.GENERIC_READ | windows.GENERIC_WRITE | windows.WRITE_DAC,
			wantShare:    windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE,
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
			if call.Access != test.wantAccess || call.Share != test.wantShare || call.Creation != test.wantCreation || call.Flags != test.wantFlags {
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

func TestWindowsTraceSinkFlushesBeforeClose(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "trace-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	order := make([]string, 0, 2)
	sink := &windowsTraceSink{
		file: file,
		flush: func(*os.File) error {
			order = append(order, "flush")
			return nil
		},
		close: func(*os.File) error {
			order = append(order, "close")
			return nil
		},
	}

	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error=%v", err)
	}
	if !reflect.DeepEqual(order, []string{"flush", "close"}) {
		t.Fatalf("close order=%v", order)
	}
}

func TestWindowsTraceMutexNameHashesCanonicalSinkIdentity(t *testing.T) {
	path := `C:\Users\trace-owner\session.jsonl`
	identity := canonicalTraceSinkIdentity(path)
	digest := sha256.Sum256([]byte(identity))
	want := `Local\shell-picker-trace-` + hex.EncodeToString(digest[:])
	got := traceMutexName(path)
	if got != want {
		t.Fatalf("mutex name=%q want %q", got, want)
	}
	if strings.Contains(got, path) || strings.Contains(got, "trace-owner") {
		t.Fatalf("mutex name exposes sink identity: %q", got)
	}
	if got := traceMutexName(`\\.\PIPE\SESSION`); got != traceMutexName(`\\.\pipe\session`) {
		t.Fatalf("named-pipe identity is not canonical: %q/%q", got, traceMutexName(`\\.\pipe\session`))
	}
}

func TestWindowsTraceSinkSessionInitialization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.jsonl")
	session := [16]byte{'a', 'b', 'c', 'd', 'e', 'f', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9'}
	if err := os.WriteFile(path, traceTestRecord(t, session, "session.start", "cp"), 0o600); err != nil {
		t.Fatal(err)
	}

	first, err := openTraceSink(path, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := openTraceSink(path, session)
	if err != nil {
		t.Fatal(err)
	}
	trace := integrationpkg.NewTrace(second, session)
	if err := trace.Event(integrationpkg.TraceEvent{Name: "generation.start", Generation: 1, Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := trace.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(data, []byte{'\n'}); got != 2 {
		t.Fatalf("record count=%d want 2; data=%q", got, data)
	}
	t.Run("truncates new or invalid sessions", windowsTraceSinkTruncatesForNewOrInvalidSession)
	t.Run("preserves and replaces across subprocess mutex lifecycles", testWindowsTraceSessionLifecycleProcesses)
}

func windowsTraceSinkTruncatesForNewOrInvalidSession(t *testing.T) {
	session := [16]byte{'a', 'b', 'c', 'd', 'e', 'f', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9'}
	other := [16]byte{'9', '8', '7', '6', '5', '4', '3', '2', '1', '0', 'f', 'e', 'd', 'c', 'b', 'a'}
	tests := []struct {
		name string
		seed []byte
	}{
		{name: "different session", seed: traceTestRecord(t, session, "session.start", "cp")},
		{name: "empty", seed: nil},
		{name: "malformed", seed: []byte(`{"session":` + "\n")},
		{name: "missing session", seed: []byte(`{"event":"stale"}` + "\n")},
		{name: "bounded prefix without complete record", seed: bytes.Repeat([]byte("x"), 4<<10)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trace.jsonl")
			if err := os.WriteFile(path, test.seed, 0o600); err != nil {
				t.Fatal(err)
			}
			sink, err := openTraceSink(path, other)
			if err != nil {
				t.Fatal(err)
			}
			trace := integrationpkg.NewTrace(sink, other)
			if err := trace.Event(integrationpkg.TraceEvent{Name: "generation.start", Generation: 1, Outcome: "ok"}); err != nil {
				t.Fatal(err)
			}
			if err := trace.Close(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := bytes.Count(data, []byte{'\n'}); got != 1 {
				t.Fatalf("record count=%d want 1; data=%q", got, data)
			}
			var record integrationpkg.TraceRecord
			if err := json.Unmarshal(bytes.TrimSpace(data), &record); err != nil {
				t.Fatal(err)
			}
			if record.Event != "generation.start" {
				t.Fatalf("record=%+v", record)
			}
		})
	}
}

func traceTestRecord(t *testing.T, session [16]byte, name, outcome string) []byte {
	t.Helper()
	var output bytes.Buffer
	trace := integrationpkg.NewTrace(&output, session)
	if err := trace.Event(integrationpkg.TraceEvent{Name: name, Outcome: outcome}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestWindowsTraceRecordWriterAcceptsAbandonedMutexAndRetriesPartialWrites(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "trace-record")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	statuses := []uint32{windows.WAIT_ABANDONED}
	releases, writes := 0, 0
	sink := &windowsTraceSink{
		file: file,
		lock: &windowsTraceRecordLock{
			handle: 42,
			wait: func(windows.Handle, uint32) (uint32, error) {
				status := statuses[0]
				statuses = statuses[1:]
				return status, nil
			},
			release: func(windows.Handle) error { releases++; return nil },
			close:   func(windows.Handle) error { return nil },
		},
		write: func(file *os.File, data []byte) (int, error) {
			writes++
			if writes == 1 {
				count := len(data) / 2
				if count == 0 {
					count = 1
				}
				if _, err := file.Write(data[:count]); err != nil {
					return 0, err
				}
				return count, io.ErrShortWrite
			}
			return file.Write(data)
		},
	}

	if err := sink.WriteRecord([]byte(`{"event":"one"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if releases != 1 || writes != 2 {
		t.Fatalf("releases=%d writes=%d, want one release and two writes", releases, writes)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"event":"one"}`+"\n" {
		t.Fatalf("record bytes=%q", data)
	}
}

func TestWindowsTraceRecordLockWaitIsBounded(t *testing.T) {
	t.Run("finite deadline", func(t *testing.T) {
		var timeout uint32
		lock := &windowsTraceRecordLock{
			handle: 42,
			wait: func(_ windows.Handle, milliseconds uint32) (uint32, error) {
				timeout = milliseconds
				return uint32(windows.WAIT_TIMEOUT), nil
			},
		}
		status, err := lock.waitForOwner()
		if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatalf("wait status=0x%x error=%v", status, err)
		}
		if timeout == 0 || timeout == windows.INFINITE {
			t.Fatalf("trace lock timeout=%d, want finite nonzero bound", timeout)
		}
	})

	t.Run("retries contention", func(t *testing.T) {
		calls := 0
		lock := &windowsTraceRecordLock{
			handle: 42,
			wait: func(_ windows.Handle, milliseconds uint32) (uint32, error) {
				calls++
				if milliseconds == 0 || milliseconds == windows.INFINITE {
					t.Fatalf("trace lock timeout=%d, want finite nonzero slice", milliseconds)
				}
				if calls == 1 {
					return uint32(windows.WAIT_TIMEOUT), nil
				}
				return windows.WAIT_OBJECT_0, nil
			},
		}
		status, err := lock.waitForOwner()
		if err != nil || status != windows.WAIT_OBJECT_0 || calls != 2 {
			t.Fatalf("wait status=0x%x calls=%d error=%v", status, calls, err)
		}
	})
}

func TestWindowsTraceRecordWriterReleasesMutexAfterWriteFailure(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "trace-record")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	releases := 0
	writeErr := errors.New("write failed")
	sink := &windowsTraceSink{
		file: file,
		lock: &windowsTraceRecordLock{
			handle:  42,
			wait:    func(windows.Handle, uint32) (uint32, error) { return windows.WAIT_OBJECT_0, nil },
			release: func(windows.Handle) error { releases++; return nil },
			close:   func(windows.Handle) error { return nil },
		},
		write: func(*os.File, []byte) (int, error) { return 0, writeErr },
	}
	if err := sink.WriteRecord([]byte("record\n")); !errors.Is(err, writeErr) {
		t.Fatalf("WriteRecord error=%v, want %v", err, writeErr)
	}
	if releases != 1 {
		t.Fatalf("releases=%d, want 1", releases)
	}
}

func TestWindowsTraceSinkCloseClosesRecordMutexOnce(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "trace-record")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	flushes, closes, mutexCloses := 0, 0, 0
	sink := &windowsTraceSink{
		file:  file,
		flush: func(*os.File) error { flushes++; return nil },
		close: func(*os.File) error { closes++; return nil },
		lock: &windowsTraceRecordLock{
			handle:  42,
			wait:    func(windows.Handle, uint32) (uint32, error) { return windows.WAIT_OBJECT_0, nil },
			release: func(windows.Handle) error { return nil },
			close:   func(windows.Handle) error { mutexCloses++; return nil },
		},
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if flushes != 1 || closes != 1 || mutexCloses != 1 {
		t.Fatalf("flushes=%d closes=%d mutexCloses=%d", flushes, closes, mutexCloses)
	}
}
