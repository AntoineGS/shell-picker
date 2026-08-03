package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerPassesArgumentsWithoutShell(t *testing.T) {
	spec := helperSpec("print-args", "a b", `$(touch nope)`, `x&y`)
	var out bytes.Buffer
	spec.Stdout = &out
	err := (Runner{}).Run(context.Background(), spec)
	if err != nil || out.String() != "a b\x00$(touch nope)\x00x&y\x00" {
		t.Fatalf("out=%q err=%v", out.String(), err)
	}
}

func TestValidateSpecRejectsMalformedEnvironmentEntries(t *testing.T) {
	for _, entry := range []string{"", "MISSING_EQUALS", "KEY\x00=value", "KEY=value\x00"} {
		t.Run(fmt.Sprintf("%q", entry), func(t *testing.T) {
			err := validateSpec(context.Background(), Spec{Path: os.Args[0], Env: []string{entry}, Containment: ContainmentOwnTree})
			if err == nil {
				t.Fatalf("environment entry %q accepted", entry)
			}
		})
	}
}

func TestExitErrorAndContextPrecedence(t *testing.T) {
	err := (Runner{}).Run(context.Background(), helperSpec("exit", "23"))
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("exit error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	child, err := (Runner{}).Start(ctx, helperSpec("block"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := child.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel wait=%v", err)
	}
}

func TestCancellationCauseAndExitPrecedePumpFailures(t *testing.T) {
	pumpErr := errors.New("writer failed")
	spec := helperSpec("print-args", "output")
	spec.Stdout = errorWriter{err: pumpErr}
	if err := (Runner{}).Run(context.Background(), spec); !errors.Is(err, pumpErr) {
		t.Fatalf("pump error=%v", err)
	}
	spec = helperSpec("exit", "19")
	spec.Stdout = errorWriter{err: pumpErr}
	err := (Runner{}).Run(context.Background(), spec)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 19 {
		t.Fatalf("exit precedence=%v", err)
	}
	cause := errors.New("caller stopped")
	ctx, cancel := context.WithCancelCause(context.Background())
	child, err := (Runner{}).Start(ctx, helperSpec("block"))
	if err != nil {
		t.Fatal(err)
	}
	cancel(cause)
	if err := child.Wait(); !errors.Is(err, cause) {
		t.Fatalf("cancellation cause=%v", err)
	}
}

func TestExitErrorPrecedesWaitDelay(t *testing.T) {
	spec := helperSpec("hold-stdout-exit")
	spec.WaitDelay = 100 * time.Millisecond
	var output bytes.Buffer
	spec.Stdout = &output
	err := (Runner{}).Run(context.Background(), spec)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 || !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("error=%v", err)
	}
	if err.Error() != exitErr.Error() {
		t.Fatalf("presentation=%q want %q", err, exitErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(output.String()))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	assertProcessGoneWithin(t, pid, 3*time.Second)
}

func TestWaitDelayAnnotationPreservesPrimaryErrors(t *testing.T) {
	observer := fmt.Errorf("%w: watcher", ErrExitObserver)
	primaries := []error{errors.New("pump"), observer, context.Canceled}
	for _, primary := range primaries {
		err := preserveWaitDelay(primary, true)
		if !errors.Is(err, primary) || !errors.Is(err, ErrWaitDelay) || err.Error() != primary.Error() ||
			primary == observer && !errors.Is(err, ErrExitObserver) {
			t.Fatalf("primary=%v annotated=%v", primary, err)
		}
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestObservationLifecycle(t *testing.T) {
	var events []ProcessEvent
	runner := Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}
	if err := runner.Run(context.Background(), helperSpec("exit", "0")); err != nil {
		t.Fatal(err)
	}
	if got := eventPhases(events); got != "attempt,start,exit" || events[0].PID != 0 || events[1].PID <= 0 || events[2].PID != events[1].PID {
		t.Fatalf("events=%+v phases=%q", events, got)
	}
}

func TestObserveAttemptWithoutStartWhenExecutableIsMissing(t *testing.T) {
	var events []ProcessEvent
	missing := t.TempDir() + string(os.PathSeparator) + "missing"
	err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}).Run(
		context.Background(), Spec{Path: missing, Containment: ContainmentOwnTree})
	if !errors.Is(err, exec.ErrNotFound) || len(events) != 1 || events[0].Phase != "attempt" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestObserveAttemptWithoutStartOnSpawnFailure(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "invalid-executable"
	if err := os.WriteFile(path, []byte("not an executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	var events []ProcessEvent
	_, err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}).Start(
		context.Background(), Spec{Path: path, Containment: ContainmentOwnTree})
	if err == nil || len(events) != 1 || events[0].Phase != "attempt" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}

func TestWaitIsSingleUseAndWaitDelayBoundsInheritedPipe(t *testing.T) {
	spec := helperSpec("hold-stdout")
	spec.WaitDelay = 100 * time.Millisecond
	var output bytes.Buffer
	spec.Stdout = &output
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("first wait=%v", err)
	}
	if err := child.Wait(); !errors.Is(err, ErrAlreadyWaited) {
		t.Fatalf("second wait=%v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output.String()))
	if err != nil {
		t.Fatalf("pid output=%q: %v", output.String(), err)
	}
	assertProcessGoneWithin(t, pid, 3*time.Second)
}

func TestWaitDelayClosesBlockingPumpedStreams(t *testing.T) {
	tests := []struct {
		name string
		spec func(*blockingStream) Spec
	}{
		{"stdin-read", func(stream *blockingStream) Spec { spec := helperSpec("exit", "0"); spec.Stdin = stream; return spec }},
		{"stdout-write", func(stream *blockingStream) Spec {
			spec := helperSpec("print-args", "x")
			spec.Stdout = stream
			return spec
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resources, goroutines := platformResourceCount(t), runtime.NumGoroutine()
			stream := newBlockingStream()
			spec := test.spec(stream)
			spec.WaitDelay = 50 * time.Millisecond
			child, err := (Runner{}).Start(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			<-stream.blocked
			if err := child.Wait(); !errors.Is(err, ErrWaitDelay) {
				t.Fatalf("wait=%v", err)
			}
			if stream.closeCalls.Load() != 1 {
				t.Fatalf("close calls=%d", stream.closeCalls.Load())
			}
			select {
			case <-child.pumpDone:
			default:
				t.Fatal("pumps still running")
			}
			assertPlatformResourcesReturn(t, resources)
			deadline := time.Now().Add(time.Second)
			for runtime.NumGoroutine() > goroutines && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if runtime.NumGoroutine() > goroutines {
				t.Fatalf("goroutines=%d baseline=%d", runtime.NumGoroutine(), goroutines)
			}
		})
	}
}

func TestCancellationClosesBlockingPumpedStream(t *testing.T) {
	stream := newBlockingStream()
	ctx, cancel := context.WithCancelCause(context.Background())
	spec := helperSpec("block")
	spec.Stdin = stream
	child, err := (Runner{}).Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	<-stream.blocked
	cause := errors.New("stop")
	cancel(cause)
	if err := child.Wait(); !errors.Is(err, cause) {
		t.Fatalf("wait=%v", err)
	}
	if stream.closeCalls.Load() != 1 {
		t.Fatalf("close calls=%d", stream.closeCalls.Load())
	}
}

func TestExitErrorPrecedesBlockingPumpWaitDelay(t *testing.T) {
	stream := newBlockingStream()
	spec := helperSpec("print-args", "x")
	spec.Stdout, spec.WaitDelay = stream, 50*time.Millisecond
	spec.Args = []string{"-test.run=^TestProcessHelper$", "--", "hold-stdout-exit"}
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	<-stream.blocked
	err = child.Wait()
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 || !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("wait=%v", err)
	}
	if stream.closeCalls.Load() != 1 {
		t.Fatalf("close calls=%d", stream.closeCalls.Load())
	}
}

func TestOrdinaryCompletionDoesNotClosePumpedCloser(t *testing.T) {
	for _, code := range []string{"0", "7"} {
		stream := &finiteCloser{Reader: strings.NewReader("input")}
		spec := helperSpec("exit", code)
		spec.Stdin = stream
		err := (Runner{}).Run(context.Background(), spec)
		if code == "0" && err != nil {
			t.Fatal(err)
		}
		if code != "0" {
			var exitErr *ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("exit=%v", err)
			}
		}
		if stream.closeCalls.Load() != 0 {
			t.Fatalf("code=%s close calls=%d", code, stream.closeCalls.Load())
		}
	}
}

func TestWaitDelayClosesSharedPointerOnce(t *testing.T) {
	stream := newBlockingStream()
	spec := helperSpec("both-streams")
	spec.Stdin, spec.Stdout, spec.Stderr, spec.WaitDelay = stream, stream, stream, 50*time.Millisecond
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	<-stream.blocked
	if err := child.Wait(); !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("wait=%v", err)
	}
	if stream.closeCalls.Load() != 1 {
		t.Fatalf("close calls=%d", stream.closeCalls.Load())
	}
	select {
	case <-child.pumpDone:
	default:
		t.Fatal("pumps still running")
	}
}

func TestKillTreeAfterWaitAndConcurrentCallsAreSafe(t *testing.T) {
	child, err := (Runner{}).Start(context.Background(), helperSpec("exit", "0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := child.KillTree(); err != nil {
		t.Fatalf("post-Wait KillTree=%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	child, err = (Runner{}).Start(ctx, helperSpec("block"))
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() { defer group.Done(); _ = child.KillTree() }()
	}
	cancel()
	waitErr := child.Wait()
	group.Wait()
	if waitErr == nil {
		t.Fatal("concurrent cancellation returned nil")
	}
}

func TestNaturalExitLinearizesBeforeLateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	child, err := (Runner{}).Start(ctx, helperSpec("exit", "0"))
	if err != nil {
		t.Fatal(err)
	}
	<-child.observedExit
	cancel()
	if err := child.Wait(); err != nil {
		t.Fatalf("late cancellation won: %v", err)
	}
}

func TestCloseStdinOnExitClosesOptedInSourceBeforeJoiningPumps(t *testing.T) {
	stream := newExitClosingReader()
	spec := helperSpec("exit", "0")
	spec.Stdin = stream
	spec.CloseStdinOnExit = true
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	<-stream.blocked
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		if err != nil || errors.Is(err, ErrWaitDelay) {
			t.Fatalf("Wait=(%v), want prompt clean exit", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait remained blocked behind opted-in stdin pump")
	}
	if got := stream.closeCalls.Load(); got != 1 {
		t.Fatalf("stdin close calls=%d, want 1", got)
	}
}

func TestCloseStdinOnExitDoesNotCloseFilesOrOutputSinks(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, stderr := &trackingWriter{}, &trackingWriter{}
	spec := helperSpec("exit", "0")
	spec.Stdin, spec.Stdout, spec.Stderr = stdin, stdout, stderr
	spec.CloseStdinOnExit = true
	if err := (Runner{}).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Stat(); err != nil {
		t.Fatalf("caller-owned stdin was closed: %v", err)
	}
	if stdout.closeCalls.Load() != 0 || stderr.closeCalls.Load() != 0 {
		t.Fatalf("output sink close calls=(%d,%d), want zero", stdout.closeCalls.Load(), stderr.closeCalls.Load())
	}
}

type blockingStream struct {
	blocked    chan struct{}
	closed     chan struct{}
	blockOnce  sync.Once
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

type exitClosingReader struct {
	blocked    chan struct{}
	closed     chan struct{}
	blockOnce  sync.Once
	closeOnce  sync.Once
	closeCalls atomic.Int32
}

func newExitClosingReader() *exitClosingReader {
	return &exitClosingReader{blocked: make(chan struct{}), closed: make(chan struct{})}
}

func (reader *exitClosingReader) Read([]byte) (int, error) {
	reader.blockOnce.Do(func() { close(reader.blocked) })
	<-reader.closed
	return 0, io.EOF
}

func (reader *exitClosingReader) Close() error {
	reader.closeCalls.Add(1)
	reader.closeOnce.Do(func() { close(reader.closed) })
	return nil
}

type trackingWriter struct {
	closeCalls atomic.Int32
}

func (writer *trackingWriter) Write(data []byte) (int, error) { return len(data), nil }
func (writer *trackingWriter) Close() error {
	writer.closeCalls.Add(1)
	return nil
}

func newBlockingStream() *blockingStream {
	return &blockingStream{blocked: make(chan struct{}), closed: make(chan struct{})}
}
func (s *blockingStream) wait() error {
	s.blockOnce.Do(func() { close(s.blocked) })
	<-s.closed
	return errors.New("stream closed")
}
func (s *blockingStream) Read([]byte) (int, error)  { return 0, s.wait() }
func (s *blockingStream) Write([]byte) (int, error) { return 0, s.wait() }
func (s *blockingStream) Close() error {
	s.closeCalls.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type finiteCloser struct {
	*strings.Reader
	closeCalls atomic.Int32
}

func (s *finiteCloser) Close() error { s.closeCalls.Add(1); return nil }

type trickyCloserState struct {
	blocked    chan struct{}
	closed     chan struct{}
	blockOnce  sync.Once
	closeOnce  sync.Once
	closeCalls atomic.Int32
}
type trickyCloser struct {
	state   *trickyCloserState
	payload any
}

func (s trickyCloser) Write([]byte) (int, error) {
	s.state.blockOnce.Do(func() { close(s.state.blocked) })
	<-s.state.closed
	return 0, errors.New("closed")
}
func (s trickyCloser) Close() error {
	s.state.closeCalls.Add(1)
	s.state.closeOnce.Do(func() { close(s.state.closed) })
	return nil
}
