//go:build windows

package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsTerminalSession struct {
	ops          windowsTerminalOps
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

	outputMu      sync.Mutex
	buffer        bytes.Buffer
	outputChanged chan struct{}
	eventMu       sync.Mutex
	events        []traceEvent
	changed       chan struct{}

	waitMu        sync.Mutex
	waitErr       error
	waitDone      chan struct{}
	drainDone     chan struct{}
	traceDone     chan struct{}
	closeOnce     sync.Once
	closeErr      error
	outputStarted bool
	traceStarted  bool
	waitStarted   bool
}

type windowsTerminalOps struct {
	closeHandle         func(windows.Handle) error
	terminateProcess    func(windows.Handle, uint32) error
	cancelIO            func(windows.Handle, *windows.Overlapped) error
	closePseudoConsole  func(windows.Handle)
	resizePseudoConsole func(windows.Handle, windows.Coord) error
	writeFile           func(windows.Handle, []byte, *uint32, *windows.Overlapped) error
	waitForSingleObject func(windows.Handle, uint32) (uint32, error)
	getExitCodeProcess  func(windows.Handle, *uint32) error
}

func defaultWindowsTerminalOps() windowsTerminalOps {
	return windowsTerminalOps{
		closeHandle: windows.CloseHandle, terminateProcess: windows.TerminateProcess, cancelIO: windows.CancelIoEx,
		closePseudoConsole: windows.ClosePseudoConsole, resizePseudoConsole: windows.ResizePseudoConsole,
		writeFile: windows.WriteFile, waitForSingleObject: windows.WaitForSingleObject,
		getExitCodeProcess: windows.GetExitCodeProcess,
	}
}

type windowsTerminalFactory struct {
	ops                 windowsTerminalOps
	createInputPipe     func() (windows.Handle, windows.Handle, error)
	createOutputPipe    func() (windows.Handle, windows.Handle, error)
	createPseudoConsole func(windows.Coord, windows.Handle, windows.Handle) (windows.Handle, error)
	createTracePipe     func() (string, windows.Handle, error)
}

func defaultWindowsTerminalFactory() windowsTerminalFactory {
	return windowsTerminalFactory{
		ops: defaultWindowsTerminalOps(),
		createInputPipe: func() (windows.Handle, windows.Handle, error) {
			var read, write windows.Handle
			err := windows.CreatePipe(&read, &write, nil, 0)
			return read, write, err
		},
		createOutputPipe: createWindowsOverlappedReadPipe,
		createPseudoConsole: func(size windows.Coord, input, output windows.Handle) (windows.Handle, error) {
			var console windows.Handle
			err := windows.CreatePseudoConsole(size, input, output, 0, &console)
			return console, err
		},
		createTracePipe: createWindowsTracePipe,
	}
}

func createWindowsTerminalResources(config terminalConfig, factory windowsTerminalFactory) (*windowsTerminalSession, string, error) {
	inputRead, inputWrite, err := factory.createInputPipe()
	if err != nil {
		return nil, "", fmt.Errorf("create ConPTY input pipe: %w", err)
	}
	outputRead, outputWrite, err := factory.createOutputPipe()
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create ConPTY output pipe: %w", err),
			factory.ops.closeHandle(inputRead), factory.ops.closeHandle(inputWrite))
	}
	console, err := factory.createPseudoConsole(windows.Coord{X: int16(config.Columns), Y: int16(config.Lines)}, inputRead, outputWrite)
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create pseudoconsole: %w", err), factory.ops.closeHandle(inputRead),
			factory.ops.closeHandle(inputWrite), factory.ops.closeHandle(outputRead), factory.ops.closeHandle(outputWrite))
	}
	session := &windowsTerminalSession{ops: factory.ops, console: console, input: inputWrite, output: outputRead,
		changed: make(chan struct{}), waitDone: make(chan struct{}), drainDone: make(chan struct{}), traceDone: make(chan struct{}),
		outputChanged: make(chan struct{})}
	if closeErr := errors.Join(factory.ops.closeHandle(inputRead), factory.ops.closeHandle(outputWrite)); closeErr != nil {
		close(session.drainDone)
		close(session.traceDone)
		return nil, "", errors.Join(fmt.Errorf("close ConPTY child pipe handles: %w", closeErr), session.Close())
	}
	tracePath, traceHandle, err := factory.createTracePipe()
	if err != nil {
		close(session.drainDone)
		close(session.traceDone)
		return nil, "", errors.Join(fmt.Errorf("create trace pipe: %w", err), session.Close())
	}
	session.trace = traceHandle
	return session, tracePath, nil
}

func newTerminalSession(t *testing.T, config terminalConfig) terminalSession {
	t.Helper()
	if build := windows.RtlGetVersion().BuildNumber; build < 17763 {
		t.Fatalf("ConPTY requires Windows build 17763 or newer, got %d", build)
	}
	session, tracePath, err := createWindowsTerminalResources(config, defaultWindowsTerminalFactory())
	if err != nil {
		t.Fatalf("create Windows terminal resources: %v", err)
	}
	session.t = t
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
	session.outputStarted, session.traceStarted = true, true
	go session.drainOutput(session.output)
	traceReady := make(chan struct{})
	go session.drainTrace(session.trace, traceReady)
	<-traceReady

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		session.Close()
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, pseudoConsoleAttributeValue(session.console), unsafe.Sizeof(session.console)); err != nil {
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
	if err := session.ops.closeHandle(information.Thread); err != nil {
		_ = session.ops.terminateProcess(information.Process, 1)
		_ = session.ops.closeHandle(information.Process)
		session.Close()
		t.Fatalf("close unused picker thread handle: %v", err)
	}
	var waitHandle windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), information.Process, windows.CurrentProcess(), &waitHandle,
		0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		_ = session.ops.terminateProcess(information.Process, 1)
		_ = session.ops.closeHandle(information.Process)
		session.Close()
		t.Fatalf("duplicate picker wait handle: %v", err)
	}
	session.process, session.pid = information.Process, int(information.ProcessId)
	session.waitStarted = true
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

func createWindowsTracePipe() (string, windows.Handle, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", 0, fmt.Errorf("random trace pipe name: %w", err)
	}
	name := `\\.\pipe\shell-picker-trace-` + hex.EncodeToString(random)
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "", 0, err
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		return "", 0, fmt.Errorf("create trace pipe security descriptor: %w", err)
	}
	handle, err := windows.CreateNamedPipe(wide, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 64<<10, 64<<10, 0, security)
	if err != nil {
		return "", 0, fmt.Errorf("create trace named pipe: %w", err)
	}
	return name, handle, nil
}

// pseudoConsoleAttributeValue passes the HPCON handle value required by
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE. Passing &console would change the ABI.
//
//go:nocheckptr
func pseudoConsoleAttributeValue(console windows.Handle) unsafe.Pointer {
	return unsafe.Pointer(console)
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
