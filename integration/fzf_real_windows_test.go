//go:build windows

package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsTerminalSession struct {
	ops                 windowsTerminalOps
	t                   *testing.T
	console             windows.Handle
	input               windows.Handle
	output              windows.Handle
	result              windows.Handle
	resultWrite         windows.Handle
	standardInput       windows.Handle
	trace               windows.Handle
	tracePath           string
	traceFactory        func(string, bool) (windows.Handle, error)
	traceMu             sync.Mutex
	traceHandles        map[windows.Handle]struct{}
	traceListeners      map[windows.Handle]struct{}
	traceIO             map[windows.Handle]*windows.Overlapped
	traceListener       windows.Handle
	traceAcceptStop     chan struct{}
	traceAcceptOnce     sync.Once
	traceAcceptorsReady chan struct{}
	traceAcceptorsOnce  sync.Once
	process             windows.Handle // control handle; waiter owns a duplicate
	pid                 int
	fzfPath             string
	argvCanaries        []string
	sidecar             bool
	recorder            *descendantRecorder
	handleMu            sync.Mutex

	outputMu      sync.Mutex
	buffer        bytes.Buffer
	firstOutputAt time.Time
	outputChanged chan struct{}
	resultMu      sync.Mutex
	resultBuffer  bytes.Buffer
	eventMu       sync.Mutex
	events        []traceEvent
	eventTimes    []time.Time
	changed       chan struct{}

	waitMu         sync.Mutex
	waitErr        error
	waitDone       chan struct{}
	drainDone      chan struct{}
	resultDone     chan struct{}
	traceDone      chan struct{}
	closeMu        sync.Mutex
	closeAttempt   chan struct{}
	closeRunning   bool
	closed         bool
	closeErr       error
	stopMu         sync.Mutex
	stop           chan struct{}
	stopOnce       sync.Once
	outputStarted  bool
	resultStarted  bool
	traceStarted   bool
	waitStarted    bool
	cleanupTimeout time.Duration
}

type windowsTerminalOps struct {
	closeHandle         func(windows.Handle) error
	terminateProcess    func(windows.Handle, uint32) error
	cancelIO            func(windows.Handle, *windows.Overlapped) error
	beforeCancelIO      func(windows.Handle)
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
	createResultPipe    func() (windows.Handle, windows.Handle, error)
	createPseudoConsole func(windows.Coord, windows.Handle, windows.Handle) (windows.Handle, error)
	createTracePipe     func() (string, windows.Handle, error)
	createTraceInstance func(string, bool) (windows.Handle, error)
}

func closeWindowsHandles(closeHandle func(windows.Handle) error, handles ...windows.Handle) (err error) {
	for _, handle := range handles {
		if handle != 0 {
			err = errors.Join(err, closeHandle(handle))
		}
	}
	return
}

func closeWindowsProcessInformation(session *windowsTerminalSession, information *windows.ProcessInformation) error {
	var err error
	if information.Thread != 0 {
		err = errors.Join(err, session.ops.closeHandle(information.Thread))
		information.Thread = 0
	}
	if information.Process != 0 {
		err = errors.Join(err, session.ops.closeHandle(information.Process))
		information.Process = 0
	}
	return err
}

func closeWindowsLaunchHandles(session *windowsTerminalSession, information *windows.ProcessInformation) error {
	var err error
	for _, handle := range []*windows.Handle{&session.resultWrite, &session.standardInput} {
		if *handle == 0 {
			continue
		}
		err = errors.Join(err, session.ops.closeHandle(*handle))
		*handle = 0
	}
	if err != nil {
		if information.Process != 0 {
			_ = session.ops.terminateProcess(information.Process, 1)
		}
		return errors.Join(err, closeWindowsProcessInformation(session, information))
	}
	if information.Thread != 0 {
		threadErr := session.ops.closeHandle(information.Thread)
		information.Thread = 0
		if threadErr != nil {
			if information.Process != 0 {
				_ = session.ops.terminateProcess(information.Process, 1)
			}
			return errors.Join(threadErr, closeWindowsProcessInformation(session, information))
		}
	}
	return nil
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
		createResultPipe: createWindowsResultPipe,
		createPseudoConsole: func(size windows.Coord, input, output windows.Handle) (windows.Handle, error) {
			var console windows.Handle
			err := windows.CreatePseudoConsole(size, input, output, 0, &console)
			return console, err
		},
		createTracePipe:     createWindowsTracePipe,
		createTraceInstance: createWindowsTracePipeInstance,
	}
}

func TestWindowsTerminalSupportsPowerShellConPTYRoot(t *testing.T) {
	term := newTerminalSession(t, terminalConfig{Path: requirePowerShell(t), Args: []string{"-NoLogo", "-NoProfile", "-NoExit", "-Command", "$function:prompt={'SP> '}"}, Environment: os.Environ(), Columns: 120, Lines: 35, DisablePickerTrace: true, TerminalOwnsStandardStreams: true})
	t.Cleanup(func() { _ = term.Close() })
	waitForCurrentScreenTextAfter(t, term, 0, "SP>")
}

func TestWindowsLaunchCleanupClosesProcessInformationHandlesAfterOwnedHandleFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		failed     windows.Handle
		failedName string
	}{
		{name: "result write", failed: 8, failedName: "result write"},
		{name: "standard input", failed: 9, failedName: "standard input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &windowsLifecycleRecorder{}
			want := errors.New("close " + test.failedName)
			ops := recorder.ops()
			closeHandle := ops.closeHandle
			failed := true
			ops.closeHandle = func(handle windows.Handle) error {
				if err := closeHandle(handle); err != nil {
					return err
				}
				if handle == test.failed && failed {
					failed = false
					return want
				}
				return nil
			}
			session := &windowsTerminalSession{ops: ops, resultWrite: 8, standardInput: 9}
			information := windows.ProcessInformation{Process: 5, Thread: 6}

			if err := closeWindowsLaunchHandles(session, &information); !errors.Is(err, want) {
				t.Fatalf("closeWindowsLaunchHandles=%v, want %v", err, want)
			}
			for _, handle := range []windows.Handle{8, 9, 5, 6} {
				if recorder.closed[handle] != 1 {
					t.Errorf("handle %d closes=%d, want one close", handle, recorder.closed[handle])
				}
			}
			if information.Process != 0 || information.Thread != 0 {
				t.Fatalf("process information retained: process=%d thread=%d", information.Process, information.Thread)
			}
			if session.resultWrite != 0 || session.standardInput != 0 {
				t.Fatalf("launch handles retained: resultWrite=%d standardInput=%d", session.resultWrite, session.standardInput)
			}
		})
	}
}

func createWindowsTerminalResources(config terminalConfig, factory windowsTerminalFactory) (*windowsTerminalSession, string, error) {
	inputRead, inputWrite, err := factory.createInputPipe()
	if err != nil {
		return nil, "", fmt.Errorf("create ConPTY input pipe: %w", err)
	}
	outputRead, outputWrite, err := factory.createOutputPipe()
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create ConPTY output pipe: %w", err), closeWindowsHandles(factory.ops.closeHandle, inputRead, inputWrite))
	}
	var resultRead, resultWrite windows.Handle
	if !config.TerminalOwnsStandardStreams {
		resultRead, resultWrite, err = factory.createResultPipe()
		if err != nil {
			return nil, "", errors.Join(fmt.Errorf("create picker result pipe: %w", err),
				closeWindowsHandles(factory.ops.closeHandle, inputRead, inputWrite, outputRead, outputWrite))
		}
	}
	console, err := factory.createPseudoConsole(windows.Coord{X: int16(config.Columns), Y: int16(config.Lines)}, inputRead, outputWrite)
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create pseudoconsole: %w", err),
			closeWindowsHandles(factory.ops.closeHandle, inputRead, inputWrite, outputRead, outputWrite, resultRead, resultWrite))
	}
	resultDone := make(chan struct{})
	if config.TerminalOwnsStandardStreams {
		close(resultDone)
	}
	traceDone := make(chan struct{})
	if config.DisablePickerTrace {
		close(traceDone)
	}
	session := &windowsTerminalSession{ops: factory.ops, console: console, input: inputWrite, output: outputRead,
		result: resultRead, resultWrite: resultWrite, changed: make(chan struct{}), waitDone: make(chan struct{}),
		drainDone: make(chan struct{}), resultDone: resultDone, traceDone: traceDone,
		outputChanged: make(chan struct{}), recorder: newDescendantRecorder(snapshotDescendantProcessRecords),
		traceHandles: make(map[windows.Handle]struct{}), traceListeners: make(map[windows.Handle]struct{}),
		traceIO:         make(map[windows.Handle]*windows.Overlapped),
		traceAcceptStop: make(chan struct{}), traceAcceptorsReady: make(chan struct{}), stop: make(chan struct{})}
	if closeErr := closeWindowsHandles(factory.ops.closeHandle, inputRead, outputWrite); closeErr != nil {
		return nil, "", errors.Join(fmt.Errorf("close ConPTY child pipe handles: %w", closeErr), session.Close())
	}
	if config.DisablePickerTrace {
		return session, "", nil
	}
	tracePath, traceHandle, err := factory.createTracePipe()
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create trace pipe: %w", err), session.Close())
	}
	session.trace = traceHandle
	session.tracePath = tracePath
	session.traceFactory = factory.createTraceInstance
	if session.traceFactory == nil {
		session.traceFactory = createWindowsTracePipeInstance
	}
	session.traceMu.Lock()
	session.traceHandles[traceHandle] = struct{}{}
	session.traceListeners[traceHandle] = struct{}{}
	session.traceListener = traceHandle
	session.traceMu.Unlock()
	return session, tracePath, nil
}

func newTerminalSession(t *testing.T, config terminalConfig) terminalSession {
	t.Helper()
	environment, err := windowsEnvironment(config.Environment)
	if err != nil {
		t.Fatalf("encode Windows environment: %v", err)
	}
	if build := windows.RtlGetVersion().BuildNumber; build < 17763 {
		t.Fatalf("ConPTY requires Windows build 17763 or newer, got %d", build)
	}
	session, tracePath, err := createWindowsTerminalResources(config, defaultWindowsTerminalFactory())
	if err != nil {
		t.Fatalf("create Windows terminal resources: %v", err)
	}
	session.t = t
	session.fzfPath = configuredFZFPath(config)
	for _, entry := range config.Environment {
		if strings.HasPrefix(strings.ToUpper(entry), "SHELL_PICKER_ADDR=") ||
			strings.HasPrefix(strings.ToUpper(entry), "SHELL_PICKER_TOKEN=") {
			_, value, _ := strings.Cut(entry, "=")
			session.argvCanaries = append(session.argvCanaries, value)
		}
	}
	session.sidecar = realFZFSidecarEnabled(config.Environment)
	session.outputStarted = true
	go session.drainOutput(session.output, session.drainDone)
	if !config.DisablePickerTrace {
		session.traceStarted = true
		traceReady := make(chan struct{})
		go session.drainTrace(session.trace, traceReady)
		<-traceReady
		if session.traceAcceptorsReady != nil {
			select {
			case <-session.traceAcceptorsReady:
			case <-session.traceAcceptStop:
				_ = session.Close()
				t.Fatal("trace acceptors stopped before picker launch")
			}
		}
	}
	session.recorder.Start()

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := updatePseudoConsoleAttribute(attributes, session.console); err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	if !config.TerminalOwnsStandardStreams {
		standardInput, err := createWindowsInheritableNullInput()
		if err != nil {
			_ = session.Close()
			t.Fatalf("create picker standard input: %v", err)
		}
		session.standardInput = standardInput
	}
	args := append([]string{config.Path}, config.Args...)
	if !config.DisablePickerTrace {
		args = append(args, "--trace", tracePath)
	}
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(args))
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	application, err := windows.UTF16PtrFromString(config.Path)
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	var directory *uint16
	if config.Directory != "" {
		directory, err = windows.UTF16PtrFromString(config.Directory)
		if err != nil {
			_ = session.Close()
			t.Fatal(err)
		}
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	if config.TerminalOwnsStandardStreams {
		startup.Flags = windows.STARTF_USESTDHANDLES
		startup.StdInput, startup.StdOutput, startup.StdErr = 0, 0, 0
	} else {
		startup.Flags = windows.STARTF_USESTDHANDLES
		startup.StdInput, startup.StdOutput, startup.StdErr = session.standardInput, session.resultWrite, session.resultWrite
	}
	var information windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	inheritHandles := !config.TerminalOwnsStandardStreams
	if err = windows.CreateProcess(application, &commandLine[0], nil, nil, inheritHandles, flags, &environment[0], directory,
		(*windows.StartupInfo)(unsafe.Pointer(&startup)), &information); err != nil {
		_ = session.Close()
		t.Fatalf("start picker in ConPTY: %v", err)
	}
	if err := closeWindowsLaunchHandles(session, &information); err != nil {
		_ = session.Close()
		t.Fatalf("close picker child handles: %v", err)
	}
	var waitHandle windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), information.Process, windows.CurrentProcess(), &waitHandle,
		0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		_ = session.ops.terminateProcess(information.Process, 1)
		_ = session.ops.closeHandle(information.Process)
		_ = session.Close()
		t.Fatalf("duplicate picker wait handle: %v", err)
	}
	session.process, session.pid = information.Process, int(information.ProcessId)
	session.recorder.SetRoot(session.pid)
	session.recorder.Capture()
	session.waitStarted = true
	session.resultStarted = !config.TerminalOwnsStandardStreams
	go session.waitProcess(waitHandle)
	if session.resultStarted {
		go session.drainResult(session.result, session.resultDone)
	}
	return session
}

func (session *windowsTerminalSession) FirstOutputAt() time.Time {
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	return session.firstOutputAt
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

func createWindowsResultPipe() (windows.Handle, windows.Handle, error) {
	read, write, err := createWindowsOverlappedReadPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := windows.SetHandleInformation(write, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		_ = windows.CloseHandle(read)
		_ = windows.CloseHandle(write)
		return 0, 0, err
	}
	return read, write, nil
}

func createWindowsInheritableNullInput() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return 0, err
	}
	security := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	return windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		security, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

func createWindowsTracePipe() (string, windows.Handle, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", 0, fmt.Errorf("random trace pipe name: %w", err)
	}
	name := `\\.\pipe\shell-picker-trace-` + hex.EncodeToString(random)
	handle, err := createWindowsTracePipeInstance(name, true)
	if err != nil {
		return "", 0, fmt.Errorf("create trace named pipe: %w", err)
	}
	return name, handle, nil
}

func createWindowsTracePipeInstance(name string, first bool) (windows.Handle, error) {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		return 0, fmt.Errorf("create trace pipe security descriptor: %w", err)
	}
	flags := uint32(windows.PIPE_ACCESS_INBOUND | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(wide, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES, 64<<10, 64<<10, 0, security)
}

var updateProcThreadAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

func updatePseudoConsoleAttribute(attributes *windows.ProcThreadAttributeListContainer, console windows.Handle) error {
	result, _, callErr := updateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(attributes.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		pseudoConsoleAttributeValue(console), unsafe.Sizeof(console), 0, 0)
	if result != 0 {
		return nil
	}
	if callErr != nil {
		return callErr
	}
	return errors.New("UpdateProcThreadAttribute failed")
}

// pseudoConsoleAttributeValue passes the HPCON handle value required by
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE. Passing the address of the handle would
// change the ABI.
func pseudoConsoleAttributeValue(console windows.Handle) uintptr {
	return uintptr(console)
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
