//go:build windows

package integration

import (
	"bytes"
	"errors"
	"fmt"
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
	waitHandle          windows.Handle // pending waiter ownership before waitProcess starts
	launchInformation   windows.ProcessInformation
	pid                 int
	rootMarker          uint64
	productionPID       int
	productionMarker    uint64
	productionIdentity  ownedProcessIdentity
	bootstrapPath       string
	productionRootPath  string
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

func prepareWindowsLaunchConfig(config terminalConfig) (terminalConfig, error) {
	if config.BootstrapPath == "" {
		if config.ProductionRootPath != "" {
			return terminalConfig{}, errors.New("ProductionRootPath requires BootstrapPath")
		}
		return config, nil
	}
	if config.ProductionRootPath == "" {
		return terminalConfig{}, errors.New("BootstrapPath requires ProductionRootPath")
	}
	config.Args = append([]string{config.Path}, config.Args...)
	config.Path = config.BootstrapPath
	return config, nil
}

func closeWindowsProcessInformation(session *windowsTerminalSession, information *windows.ProcessInformation) error {
	var err error
	if information.Thread != 0 {
		closeErr := session.ops.closeHandle(information.Thread)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			information.Thread = 0
		}
	}
	if information.Process != 0 {
		closeErr := session.ops.closeHandle(information.Process)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			information.Process = 0
		}
	}
	return err
}

func closeWindowsLaunchHandles(session *windowsTerminalSession, information *windows.ProcessInformation) error {
	var err error
	for _, handle := range []*windows.Handle{&session.resultWrite, &session.standardInput} {
		if *handle == 0 {
			continue
		}
		closeErr := session.ops.closeHandle(*handle)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			*handle = 0
		}
	}
	if err != nil {
		if information.Process != 0 {
			_ = session.ops.terminateProcess(information.Process, 1)
		}
		return errors.Join(err, closeWindowsProcessInformation(session, information))
	}
	if information.Thread != 0 {
		threadErr := session.ops.closeHandle(information.Thread)
		if threadErr == nil {
			information.Thread = 0
		}
		if threadErr != nil {
			if information.Process != 0 {
				_ = session.ops.terminateProcess(information.Process, 1)
			}
			return threadErr
		}
	}
	return nil
}

func launchWindowsProcess(session *windowsTerminalSession, start func(*windows.ProcessInformation) error) error {
	var information windows.ProcessInformation
	if err := start(&information); err != nil {
		return err
	}
	session.launchInformation = information
	return closeWindowsLaunchHandles(session, &session.launchInformation)
}

func windowsStartupInfo(standardInput, resultWrite windows.Handle) windows.StartupInfoEx {
	return windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Flags:     windows.STARTF_USESTDHANDLES,
			StdInput:  standardInput,
			StdOutput: resultWrite,
			StdErr:    resultWrite,
		},
	}
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

func createWindowsTerminalResources(config terminalConfig, factory windowsTerminalFactory) (*windowsTerminalSession, string, error) {
	inputRead, inputWrite, err := factory.createInputPipe()
	if err != nil {
		return nil, "", fmt.Errorf("create ConPTY input pipe: %w", err)
	}
	outputRead, outputWrite, err := factory.createOutputPipe()
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create ConPTY output pipe: %w", err), closeWindowsHandles(factory.ops.closeHandle, inputRead, inputWrite))
	}
	resultRead, resultWrite, err := factory.createResultPipe()
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create picker result pipe: %w", err),
			closeWindowsHandles(factory.ops.closeHandle, inputRead, inputWrite, outputRead, outputWrite))
	}
	console, err := factory.createPseudoConsole(windows.Coord{X: int16(config.Columns), Y: int16(config.Lines)}, inputRead, outputWrite)
	if err != nil {
		return nil, "", errors.Join(fmt.Errorf("create pseudoconsole: %w", err),
			closeWindowsHandles(factory.ops.closeHandle, inputRead, inputWrite, outputRead, outputWrite, resultRead, resultWrite))
	}
	resultDone := make(chan struct{})
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
	var err error
	if config, err = prepareWindowsLaunchConfig(config); err != nil {
		t.Fatalf("prepare Windows launch config: %v", err)
	}
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
	session.bootstrapPath = config.BootstrapPath
	session.productionRootPath = config.ProductionRootPath
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

	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := updatePseudoConsoleAttribute(attributes, session.console); err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	standardInput, err := createWindowsInheritableNullInput()
	if err != nil {
		_ = session.Close()
		t.Fatalf("create picker standard input: %v", err)
	}
	session.standardInput = standardInput
	if err := updateWindowsHandleList(attributes, session.standardInput, session.resultWrite); err != nil {
		_ = session.Close()
		t.Fatalf("configure inherited handles input=%#x result=%#x: %v", uintptr(session.standardInput), uintptr(session.resultWrite), err)
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
	startup := windowsStartupInfo(session.standardInput, session.resultWrite)
	startup.ProcThreadAttributeList = attributes.List()
	startup.Cb = uint32(unsafe.Sizeof(startup))
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := launchWindowsProcess(session, func(information *windows.ProcessInformation) error {
		return windows.CreateProcess(application, &commandLine[0], nil, nil, true, flags, &environment[0], directory,
			(*windows.StartupInfo)(unsafe.Pointer(&startup)), information)
	}); err != nil {
		_ = session.Close()
		t.Fatalf("start picker in ConPTY: %v", err)
	}
	var waitHandle windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), session.launchInformation.Process, windows.CurrentProcess(), &waitHandle,
		0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		_ = session.Close()
		t.Fatalf("duplicate picker wait handle: %v", err)
	}
	session.handleMu.Lock()
	session.waitHandle = waitHandle
	session.handleMu.Unlock()
	session.process, session.pid = session.launchInformation.Process, int(session.launchInformation.ProcessId)
	session.launchInformation.Process = 0
	rootMarker, err := windowsProcessCreationTime(session.process)
	if err != nil {
		_ = session.Close()
		t.Fatalf("capture bootstrap process identity: %v", err)
	}
	session.rootMarker = rootMarker
	if err := finalizeWindowsTerminalLaunch(session, config, testContext(t), windowsProductionDiscoveryDeps{}); err != nil {
		_ = session.Close()
		t.Fatalf("finish picker launch: %v", err)
	}
	return session
}
