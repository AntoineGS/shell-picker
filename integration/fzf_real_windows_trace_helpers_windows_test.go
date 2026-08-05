//go:build windows

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func openWindowsTraceClient(path string) (*os.File, error) {
	wide, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(wide, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("wrap trace client handle")
	}
	return file, nil
}

func waitForWindowsTraceRecords(session *windowsTerminalSession, callbacks int) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		session.eventMu.Lock()
		starts := countWindowsTraceEvents(session.events, "callback.info.start")
		completions := countWindowsTraceEvents(session.events, "callback.info")
		changed := session.changed
		session.eventMu.Unlock()
		if starts == callbacks && completions == callbacks {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("trace callbacks starts=%d completions=%d events=%+v", starts, completions, session.TraceEvents())
		}
		timer := time.NewTimer(remaining)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
			return fmt.Errorf("trace callbacks did not arrive: starts=%d completions=%d", starts, completions)
		}
	}
}

func waitForWindowsTraceEvent(t *testing.T, session *windowsTerminalSession, name, outcome string, count int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		session.eventMu.Lock()
		matched := 0
		for _, event := range session.events {
			if event.Event == name && event.Outcome == outcome {
				matched++
			}
		}
		changed := session.changed
		session.eventMu.Unlock()
		if matched >= count {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatalf("trace event %s/%s count=%d want=%d events=%+v", name, outcome, matched, count, session.TraceEvents())
		}
		timer := time.NewTimer(remaining)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
			t.Fatalf("trace event %s/%s did not arrive; events=%+v", name, outcome, session.TraceEvents())
		}
	}
}

func (session *windowsTerminalSession) drainTrace(handle windows.Handle, ready chan<- struct{}) {
	defer close(session.traceDone)
	session.registerTraceHandle(handle, true)
	var connections sync.WaitGroup
	var acceptors sync.WaitGroup
	var mainAssigned sync.Once
	acceptorStarted := make(chan struct{}, windowsTraceInitialListenerCount)
	initial := make([]windows.Handle, 1, windowsTraceInitialListenerCount)
	initial[0] = handle
	for range windowsTraceAcceptorCount {
		factory := session.traceFactory
		if factory == nil {
			factory = createWindowsTracePipeInstance
		}
		next, err := factory(session.tracePath, false)
		if err != nil {
			if !isTraceCancellation(err) {
				session.publishTraceError(fmt.Errorf("create trace pipe instance: %w", err))
			}
			session.stopTraceAccept()
			for _, created := range initial {
				_ = session.closeTraceHandle(created)
			}
			return
		}
		session.registerTraceHandle(next, true)
		initial = append(initial, next)
	}
	for index, initialHandle := range initial {
		acceptors.Add(1)
		initialReady := (chan<- struct{})(nil)
		if index == 0 {
			initialReady = ready
		}
		go session.runTraceAcceptor(initialHandle, &acceptors, &connections, &mainAssigned, acceptorStarted, initialReady)
	}
	if session.traceAcceptorsReady != nil {
		started := 0
		for started < windowsTraceInitialListenerCount {
			select {
			case <-acceptorStarted:
				started++
			case <-session.traceAcceptStop:
				started = windowsTraceAcceptorCount
			}
		}
		if !session.traceAcceptStopped() {
			session.traceAcceptorsOnce.Do(func() { close(session.traceAcceptorsReady) })
		}
	}
	acceptors.Wait()
	connections.Wait()
}

const windowsTraceAcceptorCount = 4
const windowsTraceInitialListenerCount = windowsTraceAcceptorCount + 1

func (session *windowsTerminalSession) runTraceAcceptor(initial windows.Handle, acceptors, connections *sync.WaitGroup,
	mainAssigned *sync.Once, started chan<- struct{}, ready chan<- struct{}) {
	defer acceptors.Done()
	for !session.traceAcceptStopped() {
		next := initial
		initial = 0
		if next == 0 {
			factory := session.traceFactory
			if factory == nil {
				factory = createWindowsTracePipeInstance
			}
			var err error
			next, err = factory(session.tracePath, false)
			if err != nil {
				if !isTraceCancellation(err) {
					session.publishTraceError(fmt.Errorf("create trace pipe instance: %w", err))
					session.stopTraceAccept()
				}
				return
			}
			session.registerTraceHandle(next, true)
		}
		if session.traceAcceptStopped() {
			_ = session.closeTraceHandle(next)
			return
		}
		startedSignal := started
		started = nil
		readySignal := ready
		ready = nil
		if err := session.connectTracePipe(next, readySignal, startedSignal); err != nil {
			session.clearTraceListener(next)
			_ = session.closeTraceHandle(next)
			if !isTraceCancellation(err) {
				session.publishTraceError(err)
				session.stopTraceAccept()
			}
			return
		}
		session.clearTraceListener(next)
		main := false
		mainAssigned.Do(func() { main = true })
		connections.Add(1)
		go session.drainTraceConnection(next, main, connections)
		if main {
			return
		}
	}
}

func (session *windowsTerminalSession) connectTracePipe(handle windows.Handle, ready, started chan<- struct{}) error {
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		if ready != nil {
			close(ready)
		}
		return err
	}
	defer session.ops.closeHandle(event)
	connectOverlapped := windows.Overlapped{HEvent: event}
	// Serialize the stop check with Close's trace-handle snapshot. This closes
	// the race where cancellation observes a registered handle before it has
	// issued ConnectNamedPipe.
	session.traceMu.Lock()
	if session.traceAcceptStopped() {
		err = windows.ERROR_OPERATION_ABORTED
	} else {
		if session.traceIO == nil {
			session.traceIO = make(map[windows.Handle]*windows.Overlapped)
		}
		session.traceIO[handle] = &connectOverlapped
		err = windows.ConnectNamedPipe(handle, &connectOverlapped)
		if !errors.Is(err, windows.ERROR_IO_PENDING) {
			delete(session.traceIO, handle)
		}
	}
	session.traceMu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if ready != nil {
		close(ready)
	}
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		var connected uint32
		err = windows.GetOverlappedResult(handle, &connectOverlapped, &connected, true)
		session.clearTraceIO(handle, &connectOverlapped)
	}
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return err
	}
	return nil
}

func (session *windowsTerminalSession) drainTraceConnection(handle windows.Handle, main bool, connections *sync.WaitGroup) {
	defer connections.Done()
	if main {
		defer session.stopTraceAccept()
	}
	defer session.closeTraceHandle(handle)

	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		session.publishTraceError(err)
		return
	}
	defer session.ops.closeHandle(event)
	buffer, pending := make([]byte, 32<<10), make([]byte, 0, 32<<10)
	for {
		if err := windows.ResetEvent(event); err != nil {
			session.publishTraceError(err)
			return
		}
		overlapped := windows.Overlapped{HEvent: event}
		var read uint32
		err := session.readTraceFile(handle, buffer, &read, &overlapped)
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			err = windows.GetOverlappedResult(handle, &overlapped, &read, true)
			session.clearTraceIO(handle, &overlapped)
		}
		if read > 0 {
			pending = append(pending, buffer[:read]...)
			for {
				newline := bytes.IndexByte(pending, '\n')
				if newline < 0 {
					break
				}
				trace, decodeErr := decodeTraceEvent(pending[:newline])
				if decodeErr != nil {
					session.publishTraceError(fmt.Errorf("%w; raw=%q", decodeErr, pending[:newline]))
					return
				}
				pending = pending[newline+1:]
				if err := session.appendTraceEvent(trace); err != nil {
					session.publishTraceError(err)
					return
				}
				session.captureDescendantSample()
			}
		}
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
			if len(pending) != 0 {
				session.publishTraceError(fmt.Errorf("%w; raw=%q", io.ErrUnexpectedEOF, pending))
			}
			return
		}
		if isTraceCancellation(err) {
			return
		}
		if err != nil {
			session.publishTraceError(err)
			return
		}
	}
}

func (session *windowsTerminalSession) appendTraceEvent(event traceEvent) error {
	stamp, err := time.Parse(time.RFC3339Nano, event.Time)
	if err != nil {
		return fmt.Errorf("parse trace event %s timestamp: %w", event.Event, err)
	}
	session.insertTraceEvent(event, stamp)
	return nil
}

func (session *windowsTerminalSession) insertTraceEvent(event traceEvent, stamp time.Time) {
	session.eventMu.Lock()
	index := sort.Search(len(session.eventTimes), func(index int) bool {
		return session.eventTimes[index].After(stamp)
	})
	session.events = append(session.events, traceEvent{})
	copy(session.events[index+1:], session.events[index:])
	session.events[index] = event
	session.eventTimes = append(session.eventTimes, time.Time{})
	copy(session.eventTimes[index+1:], session.eventTimes[index:])
	session.eventTimes[index] = stamp
	close(session.changed)
	session.changed = make(chan struct{})
	session.eventMu.Unlock()
}

func (session *windowsTerminalSession) readTraceFile(handle windows.Handle, buffer []byte, read *uint32, overlapped *windows.Overlapped) error {
	session.traceMu.Lock()
	defer session.traceMu.Unlock()
	if session.traceCloseRequested() {
		return windows.ERROR_OPERATION_ABORTED
	}
	if session.traceIO == nil {
		session.traceIO = make(map[windows.Handle]*windows.Overlapped)
	}
	session.traceIO[handle] = overlapped
	err := windows.ReadFile(handle, buffer, read, overlapped)
	if !errors.Is(err, windows.ERROR_IO_PENDING) {
		delete(session.traceIO, handle)
	}
	return err
}

func (session *windowsTerminalSession) clearTraceIO(handle windows.Handle, overlapped *windows.Overlapped) {
	session.traceMu.Lock()
	if session.traceIO[handle] == overlapped {
		delete(session.traceIO, handle)
	}
	session.traceMu.Unlock()
}

func (session *windowsTerminalSession) traceCloseRequested() bool {
	session.stopMu.Lock()
	stop := session.stop
	session.stopMu.Unlock()
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func isTraceCancellation(err error) bool {
	return errors.Is(err, windows.ERROR_OPERATION_ABORTED) || errors.Is(err, windows.ERROR_INVALID_HANDLE)
}

func (session *windowsTerminalSession) registerTraceHandle(handle windows.Handle, listener bool) {
	if handle == 0 {
		return
	}
	session.traceMu.Lock()
	if session.traceHandles == nil {
		session.traceHandles = make(map[windows.Handle]struct{})
	}
	session.traceHandles[handle] = struct{}{}
	if listener {
		if session.traceListeners == nil {
			session.traceListeners = make(map[windows.Handle]struct{})
		}
		session.traceListeners[handle] = struct{}{}
		session.traceListener = handle
	}
	session.traceMu.Unlock()
}

func (session *windowsTerminalSession) clearTraceListener(handle windows.Handle) {
	session.traceMu.Lock()
	delete(session.traceListeners, handle)
	if session.traceListener == handle {
		session.traceListener = 0
		for listener := range session.traceListeners {
			session.traceListener = listener
			break
		}
	}
	session.traceMu.Unlock()
}

func (session *windowsTerminalSession) closeTraceHandle(handle windows.Handle) error {
	if handle == 0 {
		return nil
	}
	session.traceMu.Lock()
	_, owned := session.traceHandles[handle]
	if owned {
		delete(session.traceHandles, handle)
		delete(session.traceListeners, handle)
		delete(session.traceIO, handle)
		if session.traceListener == handle {
			session.traceListener = 0
			for listener := range session.traceListeners {
				session.traceListener = listener
				break
			}
		}
	}
	session.traceMu.Unlock()
	if !owned {
		return nil
	}
	return session.ops.closeHandle(handle)
}

func (session *windowsTerminalSession) traceAcceptStopped() bool {
	if session.traceAcceptStop == nil {
		return false
	}
	select {
	case <-session.traceAcceptStop:
		return true
	default:
		return false
	}
}

func (session *windowsTerminalSession) stopTraceAccept() {
	if session.traceAcceptStop != nil {
		session.traceAcceptOnce.Do(func() { close(session.traceAcceptStop) })
	}
	session.traceMu.Lock()
	type traceOperation struct {
		handle     windows.Handle
		overlapped *windows.Overlapped
	}
	listeners := make([]traceOperation, 0, len(session.traceListeners))
	for listener := range session.traceListeners {
		listeners = append(listeners, traceOperation{handle: listener, overlapped: session.traceIO[listener]})
	}
	session.traceMu.Unlock()
	if session.ops.cancelIO != nil {
		for _, listener := range listeners {
			_ = session.ops.cancelIO(listener.handle, listener.overlapped)
		}
	}
}

func (session *windowsTerminalSession) cancelTraceIO() error {
	if session.traceAcceptStop != nil {
		session.traceAcceptOnce.Do(func() { close(session.traceAcceptStop) })
	}
	session.traceMu.Lock()
	type traceOperation struct {
		handle     windows.Handle
		overlapped *windows.Overlapped
	}
	handles := make([]traceOperation, 0, len(session.traceHandles))
	for handle := range session.traceHandles {
		handles = append(handles, traceOperation{handle: handle, overlapped: session.traceIO[handle]})
	}
	session.traceMu.Unlock()
	var err error
	for _, operation := range handles {
		if session.ops.cancelIO == nil {
			continue
		}
		cancelErr := session.ops.cancelIO(operation.handle, operation.overlapped)
		if cancelErr != nil && !errors.Is(cancelErr, windows.ERROR_NOT_FOUND) && !errors.Is(cancelErr, windows.ERROR_INVALID_HANDLE) {
			err = errors.Join(err, cancelErr)
		}
	}
	return err
}

func (session *windowsTerminalSession) publishTraceError(err error) {
	session.insertTraceEvent(traceEvent{Event: "trace.error", Outcome: err.Error()}, time.Now())
}
