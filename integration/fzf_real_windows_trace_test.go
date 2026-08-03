//go:build windows

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"golang.org/x/sys/windows"
)

func (session *windowsTerminalSession) drainTrace(handle windows.Handle, ready chan<- struct{}) {
	defer close(session.traceDone)
	defer session.ops.closeHandle(handle)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		close(ready)
		session.publishTraceError(err)
		return
	}
	defer session.ops.closeHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	err = windows.ConnectNamedPipe(handle, &overlapped)
	close(ready)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		var connected uint32
		err = windows.GetOverlappedResult(handle, &overlapped, &connected, true)
	}
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		session.publishTraceError(err)
		return
	}
	buffer, pending := make([]byte, 32<<10), make([]byte, 0, 32<<10)
	for {
		if err := windows.ResetEvent(event); err != nil {
			session.publishTraceError(err)
			return
		}
		var read uint32
		err := windows.ReadFile(handle, buffer, &read, &overlapped)
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			err = windows.GetOverlappedResult(handle, &overlapped, &read, true)
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
				session.eventMu.Lock()
				session.events = append(session.events, trace)
				close(session.changed)
				session.changed = make(chan struct{})
				session.eventMu.Unlock()
			}
		}
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
			if len(pending) != 0 {
				session.publishTraceError(fmt.Errorf("%w; raw=%q", io.ErrUnexpectedEOF, pending))
			}
			return
		}
		if err != nil {
			session.publishTraceError(err)
			return
		}
	}
}

func (session *windowsTerminalSession) publishTraceError(err error) {
	session.eventMu.Lock()
	session.events = append(session.events, traceEvent{Event: "trace.error", Outcome: err.Error()})
	close(session.changed)
	session.changed = make(chan struct{})
	session.eventMu.Unlock()
}
