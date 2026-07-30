//go:build windows

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsTerminalSession struct {
	t            *testing.T
	console      windows.Handle
	input        windows.Handle
	output       windows.Handle
	trace        windows.Handle
	process      windows.Handle // control handle; waiter owns a duplicate
	pid          int
	fzfPath      string
	argvCanaries []string
	handleMu     sync.Mutex

	outputMu sync.Mutex
	buffer   bytes.Buffer
	eventMu  sync.Mutex
	events   []traceEvent
	changed  chan struct{}

	waitMu    sync.Mutex
	waitErr   error
	waitDone  chan struct{}
	drainDone chan struct{}
	traceDone chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func newTerminalSession(t *testing.T, config terminalConfig) terminalSession {
	t.Helper()
	if build := windows.RtlGetVersion().BuildNumber; build < 17763 {
		t.Fatalf("ConPTY requires Windows build 17763 or newer, got %d", build)
	}
	inputRead, inputWrite := windows.Handle(0), windows.Handle(0)
	outputRead, outputWrite := windows.Handle(0), windows.Handle(0)
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		t.Fatalf("create ConPTY input pipe: %v", err)
	}
	outputRead, outputWrite, err := createWindowsOverlappedReadPipe()
	if err != nil {
		_ = windows.CloseHandle(inputRead)
		_ = windows.CloseHandle(inputWrite)
		t.Fatalf("create ConPTY output pipe: %v", err)
	}
	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: int16(config.Columns), Y: int16(config.Lines)}, inputRead, outputWrite, 0, &console); err != nil {
		closeWindowsHandles(inputRead, inputWrite, outputRead, outputWrite)
		t.Fatalf("CreatePseudoConsole requires Windows build 17763 or newer: %v", err)
	}
	_ = windows.CloseHandle(inputRead)
	_ = windows.CloseHandle(outputWrite)

	session := &windowsTerminalSession{t: t, console: console, input: inputWrite, output: outputRead,
		changed: make(chan struct{}), waitDone: make(chan struct{}), drainDone: make(chan struct{}), traceDone: make(chan struct{})}
	for index, argument := range config.Args {
		if argument == "--fzf" && index+1 < len(config.Args) {
			session.fzfPath = config.Args[index+1]
		}
	}
	for _, entry := range config.Environment {
		if strings.HasPrefix(strings.ToUpper(entry), "SHELL_PICKER_ADDR=") ||
			strings.HasPrefix(strings.ToUpper(entry), "SHELL_PICKER_TOKEN=") {
			_, value, _ := strings.Cut(entry, "=")
			session.argvCanaries = append(session.argvCanaries, value)
		}
	}
	go session.drainOutput(outputRead)
	tracePath, traceHandle := createWindowsTracePipe(t)
	session.trace = traceHandle
	traceReady := make(chan struct{})
	go session.drainTrace(traceHandle, traceReady)
	<-traceReady

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		session.Close()
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, unsafe.Pointer(console), unsafe.Sizeof(console)); err != nil {
		session.Close()
		t.Fatal(err)
	}
	args := append(append([]string{config.Path}, config.Args...), "--trace", tracePath)
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(args))
	if err != nil {
		session.Close()
		t.Fatal(err)
	}
	application, err := windows.UTF16PtrFromString(config.Path)
	if err != nil {
		session.Close()
		t.Fatal(err)
	}
	environment := windowsEnvironment(config.Environment)
	var directory *uint16
	if config.Directory != "" {
		directory, err = windows.UTF16PtrFromString(config.Directory)
		if err != nil {
			session.Close()
			t.Fatal(err)
		}
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var information windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(application, &commandLine[0], nil, nil, false, flags, &environment[0], directory,
		(*windows.StartupInfo)(unsafe.Pointer(&startup)), &information); err != nil {
		session.Close()
		t.Fatalf("start picker in ConPTY: %v", err)
	}
	if err := windows.CloseHandle(information.Thread); err != nil {
		_ = windows.TerminateProcess(information.Process, 1)
		_ = windows.CloseHandle(information.Process)
		session.Close()
		t.Fatalf("close unused picker thread handle: %v", err)
	}
	var waitHandle windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), information.Process, windows.CurrentProcess(), &waitHandle,
		0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		_ = windows.TerminateProcess(information.Process, 1)
		_ = windows.CloseHandle(information.Process)
		session.Close()
		t.Fatalf("duplicate picker wait handle: %v", err)
	}
	session.process, session.pid = information.Process, int(information.ProcessId)
	go session.waitProcess(waitHandle)
	return session
}

func createWindowsOverlappedReadPipe() (windows.Handle, windows.Handle, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return 0, 0, err
	}
	name := `\\.\pipe\shell-picker-conpty-output-` + hex.EncodeToString(raw)
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, 0, err
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		return 0, 0, err
	}
	server, err := windows.CreateNamedPipe(wide, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 64<<10, 64<<10, 0, security)
	if err != nil {
		return 0, 0, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	defer windows.CloseHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	err = windows.ConnectNamedPipe(server, &overlapped)
	connectPending := errors.Is(err, windows.ERROR_IO_PENDING)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	client, err := windows.CreateFile(wide, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		_ = windows.CancelIoEx(server, &overlapped)
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	if connectPending {
		var transferred uint32
		err = windows.GetOverlappedResult(server, &overlapped, &transferred, true)
	}
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(client)
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	return server, client, nil
}

func createWindowsTracePipe(t *testing.T) (string, windows.Handle) {
	t.Helper()
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		t.Fatalf("random trace pipe name: %v", err)
	}
	name := `\\.\pipe\shell-picker-trace-` + hex.EncodeToString(random)
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		t.Fatalf("create trace pipe security descriptor: %v", err)
	}
	handle, err := windows.CreateNamedPipe(wide, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 64<<10, 64<<10, 0, security)
	if err != nil {
		t.Fatalf("create trace named pipe: %v", err)
	}
	return name, handle
}

func currentUserSecurityAttributes() (*windows.SecurityAttributes, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor}, nil
}

func (session *windowsTerminalSession) drainTrace(handle windows.Handle, ready chan<- struct{}) {
	defer close(session.traceDone)
	defer windows.CloseHandle(handle)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		close(ready)
		session.publishTraceError(err)
		return
	}
	defer windows.CloseHandle(event)
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
	defer windows.CloseHandle(handle)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return
	}
	defer windows.CloseHandle(event)
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
			session.outputMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (session *windowsTerminalSession) waitProcess(handle windows.Handle) {
	defer close(session.waitDone)
	defer windows.CloseHandle(handle)
	_, err := windows.WaitForSingleObject(handle, windows.INFINITE)
	if err == nil {
		var code uint32
		err = windows.GetExitCodeProcess(handle, &code)
		if err == nil && code != 0 {
			err = fmt.Errorf("picker exited with code %d", code)
		}
	}
	session.waitMu.Lock()
	session.waitErr = err
	session.waitMu.Unlock()
	session.handleMu.Lock()
	if session.console != 0 {
		windows.ClosePseudoConsole(session.console)
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
		if err := windows.WriteFile(session.input, data, &written, nil); err != nil {
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
	return windows.ResizePseudoConsole(session.console, windows.Coord{X: int16(columns), Y: int16(lines)})
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

func (session *windowsTerminalSession) CloseInput() error {
	session.handleMu.Lock()
	defer session.handleMu.Unlock()
	if session.input == 0 {
		return nil
	}
	err := windows.CloseHandle(session.input)
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
			_ = windows.TerminateProcess(session.process, 1)
		}
		if session.console != 0 {
			windows.ClosePseudoConsole(session.console)
			session.console = 0
		}
		if session.output != 0 {
			_ = windows.CancelIoEx(session.output, nil)
		}
		if session.trace != 0 {
			_ = windows.CancelIoEx(session.trace, nil)
		}
		if session.input != 0 {
			session.closeErr = errors.Join(session.closeErr, windows.CloseHandle(session.input))
			session.input = 0
		}
		session.handleMu.Unlock()
		if session.process != 0 {
			<-session.waitDone
		}
		<-session.drainDone
		<-session.traceDone
		session.handleMu.Lock()
		if session.process != 0 {
			session.closeErr = errors.Join(session.closeErr, windows.CloseHandle(session.process))
			session.process = 0
		}
		session.output, session.trace = 0, 0
		session.handleMu.Unlock()
	})
	return session.closeErr
}

func closeWindowsHandles(handles ...windows.Handle) {
	for _, handle := range handles {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}
}
