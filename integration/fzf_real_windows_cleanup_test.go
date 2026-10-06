//go:build windows

package integration

import (
	"errors"
	"fmt"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"golang.org/x/sys/windows"
)

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
