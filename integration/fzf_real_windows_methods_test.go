//go:build windows

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"golang.org/x/sys/windows"
)

func windowsEnvironment(environment []string) []uint16 {
	block, err := process.BuildEnvironmentBlock(environment)
	if err != nil {
		panic(err)
	}
	return block
}

func TestEnvironmentBlockEncodesDriveCurrentDirectoryEntries(t *testing.T) {
	input := []string{"PATH=C:\\tools", "=X:=X:\\working", "EMPTY="}
	got := windowsEnvironment(input)
	if len(got) < 2 || got[len(got)-1] != 0 || got[len(got)-2] != 0 {
		t.Fatalf("environment block is not double terminated: %v", got)
	}
	entries := make([]string, 0, len(input))
	start := 0
	for index, value := range got[:len(got)-1] {
		if value != 0 {
			continue
		}
		entries = append(entries, windows.UTF16ToString(got[start:index]))
		start = index + 1
	}
	want := append([]string(nil), input...)
	sort.SliceStable(want, func(i, j int) bool { return strings.ToUpper(want[i]) < strings.ToUpper(want[j]) })
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("environment entries=%q want %q", entries, want)
	}
}

func (session *windowsTerminalSession) drainOutput(handle windows.Handle, done chan<- struct{}) {
	session.drainBytes(handle, done, func(data []byte) {
		session.outputMu.Lock()
		_, _ = session.buffer.Write(data)
		if session.outputChanged != nil {
			close(session.outputChanged)
		}
		session.outputChanged = make(chan struct{})
		session.outputMu.Unlock()
	})
}

func (session *windowsTerminalSession) drainResult(handle windows.Handle, done chan<- struct{}) {
	session.drainBytes(handle, done, func(data []byte) {
		session.resultMu.Lock()
		_, _ = session.resultBuffer.Write(data)
		session.resultMu.Unlock()
	})
}

func (session *windowsTerminalSession) drainBytes(handle windows.Handle, done chan<- struct{}, appendBytes func([]byte)) {
	defer close(done)
	defer session.ops.closeHandle(handle)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return
	}
	defer session.ops.closeHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	buffer := make([]byte, 32<<10)
	for {
		if err := windows.ResetEvent(event); err != nil {
			return
		}
		var read uint32
		err := windows.ReadFile(handle, buffer, &read, &overlapped)
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			err = windows.GetOverlappedResult(handle, &overlapped, &read, true)
		}
		if read > 0 {
			appendBytes(buffer[:read])
		}
		if err != nil {
			return
		}
	}
}

func (session *windowsTerminalSession) waitProcess(handle windows.Handle) {
	defer close(session.waitDone)
	defer session.ops.closeHandle(handle)
	_, err := session.ops.waitForSingleObject(handle, windows.INFINITE)
	if err == nil {
		var code uint32
		err = session.ops.getExitCodeProcess(handle, &code)
		if err == nil && code != 0 {
			err = fmt.Errorf("picker exited with code %d", code)
		}
	}
	session.waitMu.Lock()
	session.waitErr = err
	session.waitMu.Unlock()
	session.handleMu.Lock()
	if session.console != 0 {
		session.ops.closePseudoConsole(session.console)
		session.console = 0
	}
	session.handleMu.Unlock()
}

func (session *windowsTerminalSession) Send(data []byte) error {
	session.handleMu.Lock()
	defer session.handleMu.Unlock()
	if session.input == 0 {
		return os.ErrClosed
	}
	for len(data) > 0 {
		var written uint32
		if err := session.ops.writeFile(session.input, data, &written, nil); err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (session *windowsTerminalSession) Resize(columns, lines uint16) error {
	if columns == 0 || lines == 0 {
		return errors.New("terminal resize dimensions must be nonzero")
	}
	session.handleMu.Lock()
	defer session.handleMu.Unlock()
	if session.console == 0 {
		return os.ErrClosed
	}
	return session.ops.resizePseudoConsole(session.console, windows.Coord{X: int16(columns), Y: int16(lines)})
}

func (session *windowsTerminalSession) WaitBarrier(ctx context.Context, wanted barrier) traceEvent {
	session.t.Helper()
	if wanted.Count <= 0 {
		wanted.Count = 1
	}
	for {
		session.eventMu.Lock()
		count := 0
		var matched traceEvent
		for _, event := range session.events {
			if event.Event == "trace.error" {
				session.eventMu.Unlock()
				session.t.Fatalf("trace reader failed: %s", event.Outcome)
			}
			if event.Event == wanted.Event && (wanted.Operation == "" || event.Outcome == wanted.Operation) &&
				(wanted.Renderer == "" || event.Renderer == wanted.Renderer) &&
				(wanted.Generation == 0 || event.Generation == wanted.Generation) {
				count++
				matched = event
				if count >= wanted.Count {
					session.eventMu.Unlock()
					return matched
				}
			}
		}
		changed := session.changed
		session.eventMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			session.t.Fatalf("wait for barrier %+v: %v; events=%+v; output=%q", wanted, ctx.Err(), session.TraceEvents(), session.Output())
		}
	}
}

func (session *windowsTerminalSession) PID() int { return session.pid }

func (session *windowsTerminalSession) Output() []byte {
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	return bytes.Clone(session.buffer.Bytes())
}

func (session *windowsTerminalSession) ResultBytes() []byte {
	session.resultMu.Lock()
	defer session.resultMu.Unlock()
	return bytes.Clone(session.resultBuffer.Bytes())
}

func (session *windowsTerminalSession) WaitOutputAfter(ctx context.Context, before int) {
	session.t.Helper()
	for {
		session.outputMu.Lock()
		if session.buffer.Len() > before {
			session.outputMu.Unlock()
			return
		}
		if session.outputChanged == nil {
			session.outputChanged = make(chan struct{})
		}
		changed := session.outputChanged
		session.outputMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			session.t.Fatalf("wait for terminal output after %d bytes: %v; events=%+v; result=%q", before, ctx.Err(), session.TraceEvents(), session.ResultBytes())
		}
	}
}

func (session *windowsTerminalSession) CloseInput() error {
	session.handleMu.Lock()
	defer session.handleMu.Unlock()
	if session.input == 0 {
		return nil
	}
	err := session.ops.closeHandle(session.input)
	session.input = 0
	return err
}

func (session *windowsTerminalSession) Wait(ctx context.Context) error {
	select {
	case <-session.waitDone:
		select {
		case <-session.drainDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		if session.resultStarted {
			select {
			case <-session.resultDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case <-session.traceDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		session.waitMu.Lock()
		err := session.waitErr
		session.waitMu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

const defaultWindowsTerminalCleanupTimeout = 5 * time.Second

func waitForWindowsTerminalDone(done <-chan struct{}, deadline time.Time) bool {
	if done == nil {
		return true
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func windowsTerminalDone(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (session *windowsTerminalSession) cancelWorkerIO(handle *windows.Handle, done <-chan struct{}) error {
	if *handle == 0 || windowsTerminalDone(done) {
		*handle = 0
		return nil
	}
	if session.ops.beforeCancelIO != nil {
		session.ops.beforeCancelIO(*handle)
	}
	cancelErr := session.ops.cancelIO(*handle, nil)
	if cancelErr == nil || errors.Is(cancelErr, windows.ERROR_NOT_FOUND) {
		return nil
	}
	if errors.Is(cancelErr, windows.ERROR_INVALID_HANDLE) && windowsTerminalDone(done) {
		*handle = 0
		return nil
	}
	return cancelErr
}

func (session *windowsTerminalSession) Close() error {
	session.closeMu.Lock()
	if session.closed {
		err := session.closeErr
		session.closeMu.Unlock()
		return err
	}
	if session.closeRunning {
		attempt := session.closeAttempt
		session.closeMu.Unlock()
		<-attempt
		session.closeMu.Lock()
		err := session.closeErr
		session.closeMu.Unlock()
		return err
	}
	session.closeRunning = true
	session.closeAttempt = make(chan struct{})
	attempt := session.closeAttempt
	session.closeMu.Unlock()

	err := session.closeAttemptRun()

	session.closeMu.Lock()
	session.closeErr = err
	session.closeRunning = false
	if session.resourcesReleased() {
		session.closed = true
	}
	close(attempt)
	session.closeMu.Unlock()
	return err
}

func (session *windowsTerminalSession) closeAttemptRun() error {
	session.requestStop()
	session.handleMu.Lock()
	if session.process != 0 && !windowsTerminalDone(session.waitDone) {
		_ = session.ops.terminateProcess(session.process, 1)
	}
	if session.console != 0 {
		session.ops.closePseudoConsole(session.console)
		session.console = 0
	}
	var err error
	if session.output != 0 {
		if session.outputStarted {
			err = errors.Join(err, session.cancelWorkerIO(&session.output, session.drainDone))
		} else {
			err = errors.Join(err, session.ops.closeHandle(session.output))
			session.output = 0
		}
	}
	if session.result != 0 {
		if session.resultStarted {
			err = errors.Join(err, session.cancelWorkerIO(&session.result, session.resultDone))
		} else {
			err = errors.Join(err, session.ops.closeHandle(session.result))
			session.result = 0
		}
	}
	if session.resultWrite != 0 {
		err = errors.Join(err, session.ops.closeHandle(session.resultWrite))
		session.resultWrite = 0
	}
	if session.standardInput != 0 {
		err = errors.Join(err, session.ops.closeHandle(session.standardInput))
		session.standardInput = 0
	}
	if session.trace != 0 {
		if session.traceStarted {
			err = errors.Join(err, session.cancelWorkerIO(&session.trace, session.traceDone))
		} else {
			err = errors.Join(err, session.ops.closeHandle(session.trace))
			session.trace = 0
		}
	}
	if session.input != 0 {
		err = errors.Join(err, session.ops.closeHandle(session.input))
		session.input = 0
	}
	session.handleMu.Unlock()

	timeout := session.cleanupTimeout
	if timeout <= 0 {
		timeout = defaultWindowsTerminalCleanupTimeout
	}
	deadline := time.Now().Add(timeout)
	await := func(name string, started bool, done <-chan struct{}) bool {
		if started && !waitForWindowsTerminalDone(done, deadline) {
			err = errors.Join(err, fmt.Errorf("wait for %s cleanup: %w", name, process.ErrWaitDelay))
			return false
		}
		return true
	}
	processDone := await("process", session.waitStarted, session.waitDone)
	outputDone := await("output", session.outputStarted, session.drainDone)
	resultDone := await("result", session.resultStarted, session.resultDone)
	traceDone := await("trace", session.traceStarted, session.traceDone)
	session.handleMu.Lock()
	if processDone && session.process != 0 {
		err = errors.Join(err, session.ops.closeHandle(session.process))
		session.process = 0
	}
	if outputDone {
		session.output = 0
	}
	if resultDone {
		session.result = 0
	}
	if traceDone {
		session.trace = 0
	}
	session.handleMu.Unlock()
	return err
}

func (session *windowsTerminalSession) requestStop() {
	session.stopOnce.Do(func() {
		session.stop = make(chan struct{})
		close(session.stop)
	})
}

func (session *windowsTerminalSession) resourcesReleased() bool {
	session.handleMu.Lock()
	defer session.handleMu.Unlock()
	return session.process == 0 && session.output == 0 && session.result == 0 && session.trace == 0
}
