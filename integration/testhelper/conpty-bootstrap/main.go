//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		var exitErr *exitCodeError
		if errors.As(err, &exitErr) {
			os.Exit(int(exitErr.code))
		}
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 || arguments[0] == "" {
		return errors.New("missing PowerShell executable")
	}

	input, output, err := openConsoleHandles()
	if err != nil {
		return fmt.Errorf("open ConPTY console handles: %w", err)
	}
	defer windows.CloseHandle(input)
	defer windows.CloseHandle(output)
	if err := setStandardHandles(input, output); err != nil {
		return fmt.Errorf("set bootstrap standard handles: %w", err)
	}

	job, err := createKillJob()
	if err != nil {
		return fmt.Errorf("create bootstrap job: %w", err)
	}
	defer windows.CloseHandle(job)

	information, err := startChild(arguments[0], arguments[1:], input, output)
	if err != nil {
		return fmt.Errorf("start PowerShell: %w", err)
	}
	childOwned := true
	defer func() {
		if childOwned {
			terminateChild(&information)
		}
	}()

	if err := windows.AssignProcessToJobObject(job, information.Process); err != nil {
		return fmt.Errorf("assign PowerShell to bootstrap job: %w", err)
	}
	if _, err := windows.ResumeThread(information.Thread); err != nil {
		return fmt.Errorf("resume PowerShell: %w", err)
	}

	if _, err := windows.WaitForSingleObject(information.Process, windows.INFINITE); err != nil {
		return fmt.Errorf("wait for PowerShell: %w", err)
	}
	var exitCode uint32
	if err := windows.GetExitCodeProcess(information.Process, &exitCode); err != nil {
		return fmt.Errorf("get PowerShell exit code: %w", err)
	}
	if err := closeProcessInformation(&information); err != nil {
		return fmt.Errorf("close PowerShell handles: %w", err)
	}
	childOwned = false
	if exitCode != 0 {
		return &exitCodeError{code: exitCode}
	}
	return nil
}

type exitCodeError struct {
	code uint32
}

func (err *exitCodeError) Error() string {
	return fmt.Sprintf("PowerShell exited with code %d", err.code)
}

func openConsoleHandles() (windows.Handle, windows.Handle, error) {
	security := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1,
	}
	inputName, err := windows.UTF16PtrFromString("CONIN$")
	if err != nil {
		return 0, 0, err
	}
	input, err := windows.CreateFile(inputName, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, security, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, 0, err
	}
	outputName, err := windows.UTF16PtrFromString("CONOUT$")
	if err != nil {
		_ = windows.CloseHandle(input)
		return 0, 0, err
	}
	output, err := windows.CreateFile(outputName, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, security, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		_ = windows.CloseHandle(input)
		return 0, 0, err
	}
	for _, handle := range []windows.Handle{input, output} {
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			_ = windows.CloseHandle(input)
			_ = windows.CloseHandle(output)
			return 0, 0, err
		}
	}
	return input, output, nil
}

func setStandardHandles(input, output windows.Handle) error {
	if err := windows.SetStdHandle(windows.STD_INPUT_HANDLE, input); err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, output); err != nil {
		return err
	}
	return windows.SetStdHandle(windows.STD_ERROR_HANDLE, output)
}

func createKillJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func startChild(path string, arguments []string, input, output windows.Handle) (windows.ProcessInformation, error) {
	application, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	argv := append([]string{path}, arguments...)
	commandLine, err := windows.UTF16FromString(windows.ComposeCommandLine(argv))
	if err != nil {
		return windows.ProcessInformation{}, err
	}
	startup := windows.StartupInfo{
		Cb:        uint32(unsafe.Sizeof(windows.StartupInfo{})),
		Flags:     windows.STARTF_USESTDHANDLES,
		StdInput:  input,
		StdOutput: output,
		StdErr:    output,
	}
	var information windows.ProcessInformation
	if err := windows.CreateProcess(application, &commandLine[0], nil, nil, true,
		windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT, nil, nil, &startup, &information); err != nil {
		return windows.ProcessInformation{}, err
	}
	return information, nil
}

func terminateChild(information *windows.ProcessInformation) {
	if information.Process != 0 {
		_ = windows.TerminateProcess(information.Process, 1)
		_, _ = windows.WaitForSingleObject(information.Process, 5000)
	}
	_ = closeProcessInformation(information)
}

func closeProcessInformation(information *windows.ProcessInformation) error {
	var err error
	if information.Thread != 0 {
		closeErr := windows.CloseHandle(information.Thread)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			information.Thread = 0
		}
	}
	if information.Process != 0 {
		closeErr := windows.CloseHandle(information.Process)
		err = errors.Join(err, closeErr)
		if closeErr == nil {
			information.Process = 0
		}
	}
	return err
}
