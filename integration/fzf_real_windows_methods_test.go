//go:build windows

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

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
				var trace traceEvent
				if decodeErr := json.Unmarshal(pending[:newline], &trace); decodeErr != nil {
					session.publishTraceError(decodeErr)
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
				session.publishTraceError(io.ErrUnexpectedEOF)
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

func windowsEnvironment(environment []string) []uint16 {
	sorted := append([]string(nil), environment...)
	sort.SliceStable(sorted, func(i, j int) bool { return strings.ToUpper(sorted[i]) < strings.ToUpper(sorted[j]) })
	return windows.StringToUTF16(strings.Join(sorted, "\x00") + "\x00")
}

func (session *windowsTerminalSession) drainOutput(handle windows.Handle) {
	defer close(session.drainDone)
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
			session.outputMu.Lock()
			_, _ = session.buffer.Write(buffer[:read])
			if session.outputChanged != nil {
				close(session.outputChanged)
			}
			session.outputChanged = make(chan struct{})
			session.outputMu.Unlock()
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
			session.t.Fatalf("wait for barrier %+v: %v; output=%q", wanted, ctx.Err(), session.Output())
		}
	}
}

func (session *windowsTerminalSession) PID() int { return session.pid }

func (session *windowsTerminalSession) Output() []byte {
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	return bytes.Clone(session.buffer.Bytes())
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
			session.t.Fatalf("wait for terminal output after %d bytes: %v", before, ctx.Err())
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

func (session *windowsTerminalSession) Close() error {
	session.closeOnce.Do(func() {
		session.handleMu.Lock()
		if session.process != 0 {
			_ = session.ops.terminateProcess(session.process, 1)
		}
		if session.console != 0 {
			session.ops.closePseudoConsole(session.console)
			session.console = 0
		}
		if session.output != 0 {
			if session.outputStarted {
				_ = session.ops.cancelIO(session.output, nil)
			} else {
				session.closeErr = errors.Join(session.closeErr, session.ops.closeHandle(session.output))
				session.output = 0
			}
		}
		if session.trace != 0 {
			if session.traceStarted {
				_ = session.ops.cancelIO(session.trace, nil)
			} else {
				session.closeErr = errors.Join(session.closeErr, session.ops.closeHandle(session.trace))
				session.trace = 0
			}
		}
		if session.input != 0 {
			session.closeErr = errors.Join(session.closeErr, session.ops.closeHandle(session.input))
			session.input = 0
		}
		session.handleMu.Unlock()
		if session.waitStarted {
			<-session.waitDone
		}
		if session.outputStarted {
			<-session.drainDone
		}
		if session.traceStarted {
			<-session.traceDone
		}
		session.handleMu.Lock()
		if session.process != 0 {
			session.closeErr = errors.Join(session.closeErr, session.ops.closeHandle(session.process))
			session.process = 0
		}
		session.output, session.trace = 0, 0
		session.handleMu.Unlock()
	})
	return session.closeErr
}
