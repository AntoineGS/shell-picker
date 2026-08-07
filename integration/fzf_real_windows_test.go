//go:build windows

package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	launchInformation   windows.ProcessInformation
	pid                 int
	productionPID       int
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

func TestWindowsTerminalSupportsPowerShellConPTYRoot(t *testing.T) {
	powershell := requirePowerShell(t)
	term := newTerminalSession(t, terminalConfig{Path: powershell, BootstrapPath: requireConPTYBootstrap(t), ProductionRootPath: powershell, Args: []string{"-NoLogo", "-NoProfile", "-NoExit", "-Command", "$function:prompt={'SP> '}"}, Environment: os.Environ(), Columns: 120, Lines: 35, DisablePickerTrace: true})
	var windowsTerm *windowsTerminalSession
	var ok bool
	t.Cleanup(func() {
		if err := term.Close(); err != nil {
			t.Errorf("close PowerShell terminal: %v", err)
		}
		if windowsTerm != nil {
			windowsTerm.AssertNoLiveDescendants(t)
		}
	})
	waitForCurrentScreenTextAfter(t, term, 0, "SP>")
	windowsTerm, ok = term.(*windowsTerminalSession)
	if !ok {
		t.Fatalf("PowerShell terminal type=%T, want *windowsTerminalSession", term)
	}
	windowsTerm.AssertPowerShellBootstrapTopology(t)
}

func TestWindowsPrepareLaunchConfig(t *testing.T) {
	original := terminalConfig{Path: `C:\pwsh.exe`, Args: []string{"-NoLogo", "-NoExit"}, ProductionRootPath: `C:\pwsh.exe`}
	prepared, err := prepareWindowsLaunchConfig(terminalConfig{Path: original.Path, Args: original.Args, BootstrapPath: `C:\bootstrap.exe`, ProductionRootPath: original.ProductionRootPath})
	if err != nil {
		t.Fatalf("prepareWindowsLaunchConfig() error = %v", err)
	}
	if prepared.Path != `C:\bootstrap.exe` {
		t.Fatalf("prepared path = %q, want bootstrap", prepared.Path)
	}
	if !reflect.DeepEqual(prepared.Args, append([]string{original.Path}, original.Args...)) {
		t.Fatalf("prepared args = %#v, want production path followed by original args", prepared.Args)
	}
	if prepared.ProductionRootPath != original.ProductionRootPath {
		t.Fatalf("prepared production root = %q, want %q", prepared.ProductionRootPath, original.ProductionRootPath)
	}

	unchanged := terminalConfig{Path: `C:\fzf.exe`, Args: []string{"--listen"}, Environment: []string{"A=B"}, ExpectedFZFPath: `C:\fzf.exe`}
	got, err := prepareWindowsLaunchConfig(unchanged)
	if err != nil {
		t.Fatalf("prepareWindowsLaunchConfig() without bootstrap error = %v", err)
	}
	if !reflect.DeepEqual(got, unchanged) {
		t.Fatalf("config without bootstrap changed: got %#v, want %#v", got, unchanged)
	}

	if _, err := prepareWindowsLaunchConfig(terminalConfig{Path: `C:\pwsh.exe`, BootstrapPath: `C:\bootstrap.exe`}); err == nil {
		t.Fatal("prepareWindowsLaunchConfig() without production root succeeded")
	}
	if _, err := prepareWindowsLaunchConfig(terminalConfig{Path: `C:\pwsh.exe`, ProductionRootPath: `C:\pwsh.exe`}); err == nil {
		t.Fatal("prepareWindowsLaunchConfig() without bootstrap succeeded")
	}
}

func TestWindowsStartupInfoUsesHarnessHandles(t *testing.T) {
	startup := windowsStartupInfo(8, 9)
	if startup.Flags != windows.STARTF_USESTDHANDLES {
		t.Fatalf("startup flags = %#x, want %#x", startup.Flags, windows.STARTF_USESTDHANDLES)
	}
	if startup.StdInput != 8 || startup.StdOutput != 9 || startup.StdErr != 9 {
		t.Fatalf("startup standard handles = %d,%d,%d, want 8,9,9", startup.StdInput, startup.StdOutput, startup.StdErr)
	}
}

var conPTYBootstrapBinary struct {
	once sync.Once
	path string
	err  error
}

func requireConPTYBootstrap(t *testing.T) string {
	t.Helper()
	conPTYBootstrapBinary.once.Do(func() {
		repository, err := filepath.Abs("..")
		if err != nil {
			conPTYBootstrapBinary.err = err
			return
		}
		root, err := os.MkdirTemp("", "shell-picker-conpty-bootstrap-")
		if err != nil {
			conPTYBootstrapBinary.err = err
			return
		}
		conPTYBootstrapBinary.path = filepath.Join(root, binaryName("conpty-bootstrap"))
		command := exec.Command("go", "build", "-o", conPTYBootstrapBinary.path, "./integration/testhelper/conpty-bootstrap")
		command.Dir = repository
		command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
		if output, buildErr := command.CombinedOutput(); buildErr != nil {
			conPTYBootstrapBinary.err = fmt.Errorf("build ConPTY bootstrap: %w\n%s", buildErr, output)
		}
	})
	if conPTYBootstrapBinary.err != nil {
		t.Fatal(conPTYBootstrapBinary.err)
	}
	return conPTYBootstrapBinary.path
}

func TestWindowsCloseProcessInformationRetainsFailedHandleForRetry(t *testing.T) {
	for _, test := range []struct {
		name        string
		failed      windows.Handle
		wantProcess windows.Handle
		wantThread  windows.Handle
	}{
		{name: "process", failed: 5, wantProcess: 5},
		{name: "thread", failed: 6, wantThread: 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &windowsLifecycleRecorder{}
			ops := recorder.ops()
			closeHandle := ops.closeHandle
			failed := true
			want := errors.New("close process information handle")
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
			session := &windowsTerminalSession{ops: ops}
			information := windows.ProcessInformation{Process: 5, Thread: 6}

			if err := closeWindowsProcessInformation(session, &information); !errors.Is(err, want) {
				t.Fatalf("first closeWindowsProcessInformation=%v, want %v", err, want)
			}
			if information.Process != test.wantProcess || information.Thread != test.wantThread {
				t.Fatalf("handles after failed close=%d,%d, want %d,%d", information.Process, information.Thread, test.wantProcess, test.wantThread)
			}
			if err := closeWindowsProcessInformation(session, &information); err != nil {
				t.Fatalf("retry closeWindowsProcessInformation=%v", err)
			}
			if information.Process != 0 || information.Thread != 0 {
				t.Fatalf("handles retained after retry: process=%d thread=%d", information.Process, information.Thread)
			}
			for _, handle := range []windows.Handle{5, 6} {
				wantCloses := 1
				if handle == test.failed {
					wantCloses = 2
				}
				if recorder.closed[handle] != wantCloses {
					t.Errorf("handle %d closes=%d, want %d", handle, recorder.closed[handle], wantCloses)
				}
			}
		})
	}
}

func TestWindowsActualLaunchRetainsProcessInformationHandleAfterCloseFailure(t *testing.T) {
	for _, test := range []struct {
		name          string
		failed        windows.Handle
		triggerLaunch bool
		want          func(windows.ProcessInformation) windows.Handle
	}{
		{name: "process", failed: 5, triggerLaunch: true, want: func(information windows.ProcessInformation) windows.Handle { return information.Process }},
		{name: "thread", failed: 6, want: func(information windows.ProcessInformation) windows.Handle { return information.Thread }},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &windowsLifecycleRecorder{}
			ops := recorder.ops()
			closeHandle := ops.closeHandle
			failed := true
			closeLaunchHandle := true
			want := errors.New("injected launch close failure")
			ops.closeHandle = func(handle windows.Handle) error {
				if test.triggerLaunch && handle == 8 && closeLaunchHandle {
					closeLaunchHandle = false
					return want
				}
				if handle == test.failed && failed {
					failed = false
					return want
				}
				return closeHandle(handle)
			}
			session := &windowsTerminalSession{ops: ops, resultWrite: 8, standardInput: 9}

			err := launchWindowsProcess(session, func(information *windows.ProcessInformation) error {
				*information = windows.ProcessInformation{Process: 5, Thread: 6, ProcessId: 5}
				return nil
			})
			if !errors.Is(err, want) {
				t.Fatalf("launchWindowsProcess() error = %v, want injected close failure", err)
			}
			if got := test.want(session.launchInformation); got != test.failed {
				t.Fatalf("launch information retained handle = %d, want %d", got, test.failed)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("session.Close() retry = %v", err)
			}
			if session.launchInformation.Process != 0 || session.launchInformation.Thread != 0 {
				t.Fatalf("launch information after retry = %+v, want zero handles", session.launchInformation)
			}
		})
	}
}

func TestWindowsLaunchCleanupClosesProcessInformationHandlesAfterOwnedHandleFailure(t *testing.T) {
	for _, test := range []struct {
		name              string
		failed            windows.Handle
		failedName        string
		wantResultWrite   windows.Handle
		wantStandardInput windows.Handle
	}{
		{name: "result write", failed: 8, failedName: "result write", wantResultWrite: 8},
		{name: "standard input", failed: 9, failedName: "standard input", wantStandardInput: 9},
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
			if information.Process != 0 || information.Thread != 0 {
				t.Fatalf("process information retained: process=%d thread=%d", information.Process, information.Thread)
			}
			if session.resultWrite != test.wantResultWrite || session.standardInput != test.wantStandardInput {
				t.Fatalf("launch handles after failed close=%d,%d, want %d,%d", session.resultWrite, session.standardInput, test.wantResultWrite, test.wantStandardInput)
			}
			if err := session.Close(); err != nil {
				t.Fatalf("retry launch-handle cleanup: %v", err)
			}
			if session.resultWrite != 0 || session.standardInput != 0 {
				t.Fatalf("launch handles retained after retry: resultWrite=%d standardInput=%d", session.resultWrite, session.standardInput)
			}
			for _, handle := range []windows.Handle{8, 9, 5, 6} {
				wantCloses := 1
				if handle == test.failed {
					wantCloses = 2
				}
				if recorder.closed[handle] != wantCloses {
					t.Errorf("handle %d closes=%d, want %d", handle, recorder.closed[handle], wantCloses)
				}
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
	standardInput, err := createWindowsInheritableNullInput()
	if err != nil {
		_ = session.Close()
		t.Fatalf("create picker standard input: %v", err)
	}
	session.standardInput = standardInput
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
	session.process, session.pid = session.launchInformation.Process, int(session.launchInformation.ProcessId)
	session.launchInformation.Process = 0
	recordingRoot := session.pid
	if config.ProductionRootPath != "" {
		productionPID, err := waitForWindowsDescendantProcess(session.pid, config.ProductionRootPath, 30*time.Second)
		if err != nil {
			_ = session.ops.terminateProcess(session.process, 1)
			_ = session.Close()
			t.Fatalf("find expected root child: %v", err)
		}
		session.productionPID = productionPID
		recordingRoot = productionPID
	}
	session.recorder.SetRoot(recordingRoot)
	session.recorder.Capture()
	session.waitStarted = true
	session.resultStarted = true
	go session.waitProcess(waitHandle)
	go session.drainResult(session.result, session.resultDone)
	return session
}

func waitForWindowsDescendantProcess(root int, wantPath string, timeout time.Duration) (int, error) {
	wantPath, err := filepath.Abs(wantPath)
	if err != nil {
		return 0, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		nodes, err := snapshotWindowsProcesses(false)
		if err != nil {
			return 0, err
		}
		for pid := range windowsDescendantPIDs(nodes, uint32(root)) {
			node := nodes[pid]
			if node.queryErr == nil && strings.EqualFold(node.exe, wantPath) {
				return int(pid), nil
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return 0, fmt.Errorf("%s did not appear below process %d", wantPath, root)
		}
	}
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
