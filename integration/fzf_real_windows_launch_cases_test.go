//go:build windows

package integration

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

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

func TestWindowsTerminalSupportsPowerShellConPTYRootRepeated(t *testing.T) {
	powershell := requirePowerShell(t)
	bootstrap := requireConPTYBootstrap(t)
	for iteration := 0; iteration < 3; iteration++ {
		t.Run(fmt.Sprintf("iteration-%d", iteration+1), func(t *testing.T) {
			term := newTerminalSession(t, terminalConfig{
				Path: powershell, BootstrapPath: bootstrap, ProductionRootPath: powershell,
				Args:        []string{"-NoLogo", "-NoProfile", "-NoExit", "-Command", "$function:prompt={'SP> '}"},
				Environment: os.Environ(), Columns: 120, Lines: 35, DisablePickerTrace: true,
			})
			windowsTerm, ok := term.(*windowsTerminalSession)
			if !ok {
				_ = term.Close()
				t.Fatalf("PowerShell terminal type=%T, want *windowsTerminalSession", term)
			}
			waitForCurrentScreenTextAfter(t, term, 0, "SP>")
			windowsTerm.AssertPowerShellBootstrapTopology(t)
			if err := term.Close(); err != nil {
				t.Fatalf("close PowerShell terminal: %v", err)
			}
			windowsTerm.AssertNoLiveDescendants(t)
		})
	}
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

func TestWindowsHarnessHandleListExcludesInheritableCanary(t *testing.T) {
	security := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var inputRead, inputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, security, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(inputRead)
	defer windows.CloseHandle(inputWrite)
	var outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&outputRead, &outputWrite, security, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if outputWrite != 0 {
			_ = windows.CloseHandle(outputWrite)
		}
	})
	var canaryRead, canaryWrite windows.Handle
	if err := windows.CreatePipe(&canaryRead, &canaryWrite, security, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(canaryRead)
	defer windows.CloseHandle(canaryWrite)

	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := updateWindowsHandleList(attributes, inputRead, outputWrite); err != nil {
		t.Fatal(err)
	}
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Flags: windows.STARTF_USESTDHANDLES, StdInput: inputRead, StdOutput: outputWrite, StdErr: outputWrite}, ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine([]string{os.Args[0], "-test.run=^TestWindowsHarnessHandleProbe$", "canary=" + strconv.FormatUint(uint64(canaryRead), 10)}))
	if err != nil {
		t.Fatal(err)
	}
	application, err := windows.UTF16PtrFromString(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	var information windows.ProcessInformation
	if err := windows.CreateProcess(application, &commandLine[0], nil, nil, true, windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, nil, nil, (*windows.StartupInfo)(unsafe.Pointer(&startup)), &information); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.ResumeThread(information.Thread); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.WaitForSingleObject(information.Process, windows.INFINITE); err != nil {
		t.Fatal(err)
	}
	if err := closeWindowsProcessInformation(&windowsTerminalSession{ops: defaultWindowsTerminalOps()}, &information); err != nil {
		t.Fatal(err)
	}
	_ = windows.CloseHandle(outputWrite)
	outputWrite = 0
	probeOutput := os.NewFile(uintptr(outputRead), "probe-output")
	defer probeOutput.Close()
	line, err := io.ReadAll(probeOutput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(line)), "false") {
		t.Fatalf("canary inheritance result=%q, want false", line)
	}
}

func TestWindowsHarnessHandleListAcceptsProductionHandles(t *testing.T) {
	standardInput, err := createWindowsInheritableNullInput()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(standardInput)
	resultRead, resultWrite, err := createWindowsResultPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(resultRead)
	defer windows.CloseHandle(resultWrite)
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := updateWindowsHandleList(attributes, standardInput, resultWrite); err != nil {
		t.Fatalf("update production handle list: %v", err)
	}
}

func TestWindowsHarnessHandleListCombinesWithPseudoConsole(t *testing.T) {
	factory := defaultWindowsTerminalFactory()
	inputRead, inputWrite, err := factory.createInputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(inputRead)
	defer windows.CloseHandle(inputWrite)
	outputRead, outputWrite, err := factory.createOutputPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(outputRead)
	defer windows.CloseHandle(outputWrite)
	console, err := factory.createPseudoConsole(windows.Coord{X: 80, Y: 24}, inputRead, outputWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.ClosePseudoConsole(console)
	standardInput, err := createWindowsInheritableNullInput()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(standardInput)
	resultRead, resultWrite, err := createWindowsResultPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(resultRead)
	defer windows.CloseHandle(resultWrite)
	attributes, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	if err := updatePseudoConsoleAttribute(attributes, console); err != nil {
		t.Fatal(err)
	}
	if err := updateWindowsHandleList(attributes, standardInput, resultWrite); err != nil {
		t.Fatalf("update combined attributes: %v", err)
	}
}

func TestWindowsHarnessHandleProbe(t *testing.T) {
	for _, argument := range os.Args[1:] {
		if strings.HasPrefix(argument, "canary=") {
			value, err := strconv.ParseUint(strings.TrimPrefix(argument, "canary="), 10, strconv.IntSize)
			if err != nil {
				t.Fatal(err)
			}
			fileType, err := windows.GetFileType(windows.Handle(value))
			if err != nil {
				fileType = windows.FILE_TYPE_UNKNOWN
			}
			_, _ = fmt.Fprintln(os.Stdout, fileType != windows.FILE_TYPE_UNKNOWN)
			return
		}
	}
}
