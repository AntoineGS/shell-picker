package preview

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	processpkg "github.com/AntoineGS/shell-picker/internal/process"
)

func TestCountingWriterReturnsOutputLimitWithoutExceedingBound(t *testing.T) {
	var destination bytes.Buffer
	writer := newCountingWriter(&destination, 4)
	n, err := writer.Write([]byte("abcdef"))
	if n != 4 || !errors.Is(err, ErrOutputLimit) || destination.String() != "abcd" {
		t.Fatalf("n=%d err=%v output=%q", n, err, destination.String())
	}
	if n, err = writer.Write([]byte("z")); n != 0 || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("second write n=%d err=%v", n, err)
	}
}

func TestNormalizedLimitsRestoreSecurityDefaults(t *testing.T) {
	got := normalizedLimits(Limits{})
	if got != DefaultLimits {
		t.Fatalf("got %+v want %+v", got, DefaultLimits)
	}
}

func TestOutputBudgetAggregatesConcurrentDestinations(t *testing.T) {
	var first, second bytes.Buffer
	var limitCalls atomic.Int32
	budget := newOutputBudget(10, func() { limitCalls.Add(1) })
	writers := []*budgetWriter{budget.writer(&first), budget.writer(&second)}
	errorsSeen := make(chan error, len(writers))
	var group sync.WaitGroup
	for _, writer := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := writer.Write([]byte("12345678"))
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	limited := false
	for err := range errorsSeen {
		limited = limited || errors.Is(err, ErrOutputLimit)
	}
	written, exceeded := budget.status()
	if written != 10 || first.Len()+second.Len() != 10 || !exceeded || !limited || limitCalls.Load() != 1 {
		t.Fatalf("written=%d destinations=%d exceeded=%v limited=%v callbacks=%d", written,
			first.Len()+second.Len(), exceeded, limited, limitCalls.Load())
	}
}

type fakeTreeHandle struct{ kills, closes int }

func (handle *fakeTreeHandle) KillTree() error { handle.kills++; return nil }
func (handle *fakeTreeHandle) Close() error    { handle.closes++; return nil }

type cancelWriter struct {
	cancel context.CancelFunc
	writes int
}

func (writer *cancelWriter) Write(data []byte) (int, error) {
	writer.writes++
	writer.cancel()
	return len(data), nil
}

func TestNativeResourceFailureAfterOrdinaryChildIsTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixture is Unix-specific")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "bat"), []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"output", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output bytes.Buffer
			var destination = interface{ Write([]byte) (int, error) }(&output)
			if mode == "deadline" {
				destination = &cancelWriter{cancel: cancel}
			}
			tree := &fakeTreeHandle{}
			options := Options{Columns: 80, Lines: 40, Environment: []string{"PATH=" + tools}, Runner: processpkg.Runner{},
				Limits: DefaultLimits, Stdout: destination, Stderr: &bytes.Buffer{}}
			options.retainTree = func(*processpkg.Child) (treeHandle, error) { return tree, nil }
			if mode == "output" {
				options.Limits.MaxOutputBytes = 8
			}
			err := Render(ctx, resolved(path), options)
			if !errors.Is(err, ErrTerminalResource) || tree.kills != 1 || tree.closes != 1 {
				t.Fatalf("err=%v tree=%+v", err, tree)
			}
		})
	}
}

func TestFileHintedZipNativeResourceFailureIsTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct executable fixture is Unix-specific")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "file"), []byte("#!/bin/sh\nprintf application/zip\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "unknown")
	if err := os.WriteFile(path, []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	tree := &fakeTreeHandle{}
	options := Options{Columns: 80, Lines: 40, Environment: []string{"PATH=" + tools}, Runner: processpkg.Runner{},
		Limits: DefaultLimits, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	options.Limits.MaxOutputBytes = 20
	options.retainTree = func(*processpkg.Child) (treeHandle, error) { return tree, nil }
	err := Render(context.Background(), resolved(path), options)
	if !errors.Is(err, ErrTerminalResource) || tree.kills != 1 || tree.closes != 1 {
		t.Fatalf("err=%v tree=%+v", err, tree)
	}
}

func TestCombinedWaitDelayIsTerminalResource(t *testing.T) {
	exitErr := &processpkg.ExitError{Code: 17}
	combined := errors.Join(exitErr, processpkg.ErrWaitDelay)
	tree := &fakeTreeHandle{}
	session := &renderSession{tree: tree, started: true}
	cause := resourceFailure(context.Background(), newOutputBudget(1, nil), combined)
	err := session.terminal(cause)
	session.close()
	var gotExit *processpkg.ExitError
	if !errors.Is(err, ErrTerminalResource) || !errors.Is(err, processpkg.ErrWaitDelay) ||
		!errors.As(err, &gotExit) || gotExit.ExitCode() != 17 || tree.kills != 1 || tree.closes != 1 {
		t.Fatalf("err=%v tree=%+v", err, tree)
	}
}

func TestExitErrorWithoutWaitDelayRemainsOrdinary(t *testing.T) {
	err := resourceFailure(context.Background(), newOutputBudget(1, nil), &processpkg.ExitError{Code: 17})
	if err != nil {
		t.Fatalf("ordinary exit classified as resource: %v", err)
	}
}
