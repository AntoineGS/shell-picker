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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"golang.org/x/sys/windows"
)

func windowsEnvironment(environment []string) ([]uint16, error) {
	return process.BuildEnvironmentBlock(environment)
}

func TestEnvironmentBlockEncodesDriveCurrentDirectoryEntries(t *testing.T) {
	input := []string{"PATH=C:\\tools", "=X:=X:\\working", "EMPTY="}
	got, err := windowsEnvironment(input)
	if err != nil {
		t.Fatal(err)
	}
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
		if session.firstOutputAt.IsZero() {
			session.firstOutputAt = time.Now()
		}
		_, _ = session.buffer.Write(data)
		if session.outputChanged != nil {
			close(session.outputChanged)
		}
		session.outputChanged = make(chan struct{})
		session.outputMu.Unlock()
		session.captureDescendantSample()
	})
}

func (session *windowsTerminalSession) drainResult(handle windows.Handle, done chan<- struct{}) {
	session.drainBytes(handle, done, func(data []byte) {
		session.resultMu.Lock()
		_, _ = session.resultBuffer.Write(data)
		session.resultMu.Unlock()
		session.captureDescendantSample()
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
	buffer := make([]byte, 32<<10)
	for {
		if err := windows.ResetEvent(event); err != nil {
			return
		}
		overlapped := windows.Overlapped{HEvent: event}
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
			if matchesTraceBarrier(event, wanted) {
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
			events := session.TraceEvents()
			session.t.Fatalf("wait for barrier %+v: %v; sidecar=%s; events=%+v; output=%q", wanted, ctx.Err(), sidecarDiagnostics(events), events, session.Output())
		}
	}
}

func (session *windowsTerminalSession) productionRootPID() int {
	if session.productionPID != 0 {
		return session.productionPID
	}
	return session.pid
}

func (session *windowsTerminalSession) processTreeRoot() windowsProcessIdentityKey {
	if session.productionPID != 0 {
		return windowsProcessIdentityKey{pid: uint32(session.productionPID), marker: session.productionMarker}
	}
	return windowsProcessIdentityKey{pid: uint32(session.pid), marker: session.rootMarker}
}

func (session *windowsTerminalSession) PID() int { return session.productionRootPID() }

func waitForWindowsProcessTreeIdentityExit(root windowsProcessIdentityKey, deadline time.Time, snapshot func() (map[uint32]windowsProcessNode, error)) error {
	return waitForWindowsProcessTreeIdentityExitSeeded(root, deadline, snapshot, nil)
}

func waitForWindowsProcessTreeIdentityExitSeeded(root windowsProcessIdentityKey, deadline time.Time, snapshot func() (map[uint32]windowsProcessNode, error), observed []windowsProcessIdentityKey) error {
	if snapshot == nil {
		snapshot = func() (map[uint32]windowsProcessNode, error) {
			return snapshotWindowsProcesses(false)
		}
	}
	tracker := windowsProcessTreeTracker{root: root, observed: make(map[windowsProcessIdentityKey]struct{}, len(observed))}
	for _, key := range observed {
		tracker.observed[key] = struct{}{}
	}
	for {
		nodes, err := snapshot()
		if err != nil {
			return err
		}
		if !tracker.observe(nodes) {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("wait for process tree rooted at %d: %w", root.pid, process.ErrWaitDelay)
		}
		interval := 10 * time.Millisecond
		if remaining < interval {
			interval = remaining
		}
		timer := time.NewTimer(interval)
		<-timer.C
	}
}

func (session *windowsTerminalSession) observedProcessIdentityKeys() []windowsProcessIdentityKey {
	if session.recorder == nil {
		return nil
	}
	keys := make([]windowsProcessIdentityKey, 0)
	for _, record := range session.recorder.Records() {
		pidText, markerText, ok := strings.Cut(record.Identity, ":")
		pid, pidErr := strconv.ParseUint(pidText, 10, 32)
		marker, markerErr := strconv.ParseUint(markerText, 10, 64)
		if ok && pidErr == nil && markerErr == nil {
			keys = append(keys, windowsProcessIdentityKey{pid: uint32(pid), marker: marker})
		}
	}
	return keys
}

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
		session.stopDescendantRecorder()
		session.waitMu.Lock()
		err := session.waitErr
		session.waitMu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

const defaultWindowsTerminalCleanupTimeout = 5 * time.Second
const defaultWindowsTerminalForceCleanupTimeout = time.Second

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

func waitForWindowsProcessTermination(ops windowsTerminalOps, handle windows.Handle, deadline time.Time) (bool, error) {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false, process.ErrWaitDelay
	}
	timeout := uint32(remaining / time.Millisecond)
	if timeout == 0 {
		timeout = 1
	}
	status, err := ops.waitForSingleObject(handle, timeout)
	if err != nil {
		return false, err
	}
	if status != windows.WAIT_OBJECT_0 {
		return false, process.ErrWaitDelay
	}
	return true, nil
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
	traceGraceful := session.traceStarted && session.traceHandles != nil
	if !traceGraceful {
		session.requestStop()
	}
	timeout := session.cleanupTimeout
	if timeout <= 0 {
		timeout = defaultWindowsTerminalCleanupTimeout
	}
	deadline := time.Now().Add(timeout)
	var preWaitProcess windows.Handle
	outputDone, resultDone, traceDone := true, true, true
	observedProcessIdentities := session.observedProcessIdentityKeys()
	if session.pid != 0 {
		if sampled, sampleErr := snapshotWindowsProcessTreeIdentityKeys(session.processTreeRoot()); sampleErr == nil {
			observedProcessIdentities = append(observedProcessIdentities, sampled...)
		}
	}
	session.handleMu.Lock()
	if session.process != 0 && !session.waitStarted {
		preWaitProcess = session.process
		_ = session.ops.terminateProcess(session.process, 1)
	} else if session.process != 0 && !windowsTerminalDone(session.waitDone) {
		_ = session.ops.terminateProcess(session.process, 1)
	}
	if session.launchInformation.Process != 0 {
		if preWaitProcess == 0 {
			preWaitProcess = session.launchInformation.Process
		}
		_ = session.ops.terminateProcess(session.launchInformation.Process, 1)
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
			closeErr := session.ops.closeHandle(session.output)
			err = errors.Join(err, closeErr)
			if closeErr == nil {
				session.output = 0
			} else {
				outputDone = false
			}
		}
	}
	if session.result != 0 {
		if session.resultStarted {
			err = errors.Join(err, session.cancelWorkerIO(&session.result, session.resultDone))
		} else {
			closeErr := session.ops.closeHandle(session.result)
			err = errors.Join(err, closeErr)
			if closeErr == nil {
				session.result = 0
			} else {
				resultDone = false
			}
		}
	}
	if session.resultWrite != 0 {
		closeErr := session.ops.closeHandle(session.resultWrite)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			session.resultWrite = 0
		}
	}
	if session.standardInput != 0 {
		closeErr := session.ops.closeHandle(session.standardInput)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			session.standardInput = 0
		}
	}
	if session.trace != 0 {
		if session.traceStarted {
			if session.traceHandles == nil {
				err = errors.Join(err, session.cancelWorkerIO(&session.trace, session.traceDone))
			} else {
				session.stopTraceAccept()
			}
		} else {
			if session.traceHandles == nil {
				closeErr := session.ops.closeHandle(session.trace)
				err = errors.Join(err, closeErr)
				if closeErr == nil {
					session.trace = 0
				} else {
					traceDone = false
				}
			} else {
				err = errors.Join(err, session.closeTraceHandle(session.trace))
				if session.trace != 0 {
					traceDone = false
				}
			}
		}
	}
	if session.input != 0 {
		closeErr := session.ops.closeHandle(session.input)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			session.input = 0
		}
	}
	session.handleMu.Unlock()

	await := func(name string, started bool, done <-chan struct{}) bool {
		if started && !waitForWindowsTerminalDone(done, deadline) {
			err = errors.Join(err, fmt.Errorf("wait for %s cleanup: %w", name, process.ErrWaitDelay))
			return false
		}
		return true
	}
	processDone := true
	if preWaitProcess != 0 {
		var waitErr error
		processDone, waitErr = waitForWindowsProcessTermination(session.ops, preWaitProcess, deadline)
		err = errors.Join(err, waitErr)
		if !processDone {
			err = errors.Join(err, fmt.Errorf("wait for process termination: %w", process.ErrWaitDelay))
		}
	} else {
		processDone = await("process", session.waitStarted, session.waitDone)
	}
	if session.outputStarted {
		outputDone = await("output", true, session.drainDone)
	}
	if session.resultStarted {
		resultDone = await("result", true, session.resultDone)
	}
	if session.traceStarted {
		traceDone = waitForWindowsTerminalDone(session.traceDone, deadline)
		if !traceDone && traceGraceful {
			session.requestStop()
			err = errors.Join(err, session.cancelTraceIO())
			traceDone = waitForWindowsTerminalDone(session.traceDone, time.Now().Add(defaultWindowsTerminalForceCleanupTimeout))
		}
		if !traceDone {
			err = errors.Join(err, fmt.Errorf("wait for trace cleanup: %w", process.ErrWaitDelay))
		}
	}
	if traceGraceful && !traceDone {
		session.requestStop()
		err = errors.Join(err, session.cancelTraceIO())
	}
	treeDone := true
	if processDone && session.pid != 0 {
		treeErr := waitForWindowsProcessTreeIdentityExitSeeded(session.processTreeRoot(), deadline, nil, observedProcessIdentities)
		err = errors.Join(err, treeErr)
		treeDone = treeErr == nil
	}
	session.stopDescendantRecorder()
	session.handleMu.Lock()
	if processDone && treeDone && session.waitHandle != 0 {
		closeErr := session.ops.closeHandle(session.waitHandle)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			session.waitHandle = 0
		}
	}
	if processDone && treeDone && session.process != 0 {
		closeErr := session.ops.closeHandle(session.process)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			session.process = 0
		}
	}
	if processDone && treeDone {
		err = errors.Join(err, closeWindowsProcessInformation(session, &session.launchInformation))
		if session.productionIdentity != nil {
			identityErr := session.productionIdentity.Close()
			err = errors.Join(err, identityErr)
			if identityErr == nil {
				session.productionIdentity = nil
			}
		}
	}
	if outputDone && session.outputStarted {
		session.output = 0
	}
	if resultDone && session.resultStarted {
		session.result = 0
	}
	if traceDone && session.traceStarted {
		session.trace = 0
	}
	session.handleMu.Unlock()
	return err
}

func (session *windowsTerminalSession) requestStop() {
	session.stopMu.Lock()
	if session.stop == nil {
		session.stop = make(chan struct{})
	}
	stop := session.stop
	session.stopMu.Unlock()
	session.stopOnce.Do(func() { close(stop) })
}

func (session *windowsTerminalSession) resourcesReleased() bool {
	session.handleMu.Lock()
	released := session.process == 0 && session.waitHandle == 0 && session.launchInformation.Process == 0 && session.launchInformation.Thread == 0 &&
		session.resultWrite == 0 && session.standardInput == 0 && session.input == 0 &&
		session.productionIdentity == nil &&
		session.output == 0 && session.result == 0 && session.trace == 0
	session.handleMu.Unlock()
	if !released {
		return false
	}
	session.traceMu.Lock()
	defer session.traceMu.Unlock()
	return len(session.traceHandles) == 0 && len(session.traceListeners) == 0 && session.traceListener == 0
}
