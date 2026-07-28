//go:build windows

package process

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPreparedStreamsPumpCount(t *testing.T) {
	spec := helperSpec("exit", "0")
	spec.Stdout, spec.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if child.pumpCount() != 2 {
		t.Fatalf("pump count=%d", child.pumpCount())
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsStartFailureStagesCloseEverything(t *testing.T) {
	for _, stage := range allWinStages() {
		t.Run(stage.String(), func(t *testing.T) {
			baseline := processHandleCount(t)
			err, childPID := startWithInjectedFailure(stage)
			if err == nil {
				t.Fatal("injected stage succeeded")
			}
			assertHandleCountReturns(t, baseline)
			if childPID != 0 {
				assertProcessGoneWithin(t, childPID, 2*time.Second)
			}
		})
	}
}

type winStage uint8

const (
	stageDevNull winStage = iota + 1
	stageDuplicate
	stagePipe
	stagePipeInheritance
	stageCreateJob
	stageConfigureJob
	stageAttributeList
	stageUpdateHandleList
	stageCreateProcess
	stageAssignJob
	stageResumeThread
)

func allWinStages() []winStage {
	return []winStage{stageDevNull, stageDuplicate, stagePipe, stagePipeInheritance, stageCreateJob,
		stageConfigureJob, stageAttributeList, stageUpdateHandleList, stageCreateProcess, stageAssignJob, stageResumeThread}
}

func (s winStage) String() string {
	return []string{"", "devnull", "duplicate", "pipe", "pipe-inheritance", "create-job", "configure-job",
		"attribute-list", "update-handle-list", "create-process", "assign-job", "resume-thread"}[s]
}

func startWithInjectedFailure(stage winStage) (error, int) {
	fail := syscall.Errno(windows.ERROR_INVALID_FUNCTION)
	oldCreateFile, oldDuplicate, oldPipe := winCreateFile, winDuplicateHandle, winCreatePipe
	oldSetHandle, oldCreateJob, oldSetJob := winSetHandleInformation, winCreateJobObject, winSetInformationJobObject
	oldAttributes, oldUpdate := winNewAttributeList, winUpdateHandleList
	oldCreateProcess, oldAssign, oldResume := winCreateProcess, winAssignProcessToJobObject, winResumeThread
	childPID := 0
	defer func() {
		winCreateFile, winDuplicateHandle, winCreatePipe = oldCreateFile, oldDuplicate, oldPipe
		winSetHandleInformation, winCreateJobObject, winSetInformationJobObject = oldSetHandle, oldCreateJob, oldSetJob
		winNewAttributeList, winUpdateHandleList = oldAttributes, oldUpdate
		winCreateProcess, winAssignProcessToJobObject, winResumeThread = oldCreateProcess, oldAssign, oldResume
	}()
	winCreateProcess = func(app, command *uint16, processSA, threadSA *windows.SecurityAttributes, inherit bool,
		flags uint32, environment, directory *uint16, startup *windows.StartupInfo, info *windows.ProcessInformation) error {
		err := oldCreateProcess(app, command, processSA, threadSA, inherit, flags, environment, directory, startup, info)
		if err == nil {
			childPID = int(info.ProcessId)
		}
		return err
	}
	switch stage {
	case stageDevNull:
		winCreateFile = func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			return 0, fail
		}
	case stageDuplicate:
		winDuplicateHandle = func(windows.Handle, windows.Handle, windows.Handle, *windows.Handle, uint32, bool, uint32) error {
			return fail
		}
	case stagePipe:
		winCreatePipe = func(*windows.Handle, *windows.Handle, *windows.SecurityAttributes, uint32) error { return fail }
	case stagePipeInheritance:
		winSetHandleInformation = func(windows.Handle, uint32, uint32) error { return fail }
	case stageCreateJob:
		winCreateJobObject = func(*windows.SecurityAttributes, *uint16) (windows.Handle, error) { return 0, fail }
	case stageConfigureJob:
		winSetInformationJobObject = func(windows.Handle, uint32, uintptr, uint32) (int, error) { return 0, fail }
	case stageAttributeList:
		winNewAttributeList = func(uint32) (*windows.ProcThreadAttributeListContainer, error) { return nil, fail }
	case stageUpdateHandleList:
		winUpdateHandleList = func(*windows.ProcThreadAttributeListContainer, []windows.Handle) error { return fail }
	case stageCreateProcess:
		winCreateProcess = func(*uint16, *uint16, *windows.SecurityAttributes, *windows.SecurityAttributes, bool, uint32,
			*uint16, *uint16, *windows.StartupInfo, *windows.ProcessInformation) error {
			return fail
		}
	case stageAssignJob:
		winAssignProcessToJobObject = func(windows.Handle, windows.Handle) error { return fail }
	case stageResumeThread:
		winResumeThread = func(windows.Handle) (uint32, error) { return 0, fail }
	}
	spec := helperSpec("block")
	spec.Stdin, spec.Stdout, spec.Stderr = bytes.NewReader(nil), nil, &bytes.Buffer{}
	_, err := (Runner{}).Start(context.Background(), spec)
	return err, childPID
}

func TestCleanupFailuresDoNotMaskStartFailure(t *testing.T) {
	primary, cleanup := errors.New("assign failed"), errors.New("cleanup failed")
	cleanupPhase := false
	var terminates, waits, closes atomic.Int32
	oldAssign, oldTerminate, oldWait, oldClose := winAssignProcessToJobObject, winTerminateProcess, winWaitForSingleObject, winCloseHandle
	defer func() {
		winAssignProcessToJobObject, winTerminateProcess, winWaitForSingleObject, winCloseHandle = oldAssign, oldTerminate, oldWait, oldClose
	}()
	winAssignProcessToJobObject = func(windows.Handle, windows.Handle) error { cleanupPhase = true; return primary }
	winTerminateProcess = func(handle windows.Handle, code uint32) error {
		terminates.Add(1)
		_ = oldTerminate(handle, code)
		return cleanup
	}
	winWaitForSingleObject = func(handle windows.Handle, timeout uint32) (uint32, error) {
		waits.Add(1)
		_, _ = oldWait(handle, timeout)
		return 0, cleanup
	}
	winCloseHandle = func(handle windows.Handle) error {
		err := oldClose(handle)
		if cleanupPhase {
			closes.Add(1)
			return cleanup
		}
		return err
	}
	_, err := (Runner{}).Start(context.Background(), helperSpec("block"))
	if !errors.Is(err, primary) {
		t.Fatalf("error=%v", err)
	}
	if terminates.Load() != 1 || waits.Load() != 1 || closes.Load() < 6 {
		t.Fatalf("cleanup attempts terminate=%d wait=%d close=%d", terminates.Load(), waits.Load(), closes.Load())
	}
}

var getProcessHandleCount = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")

func processHandleCount(t *testing.T) uint32 {
	t.Helper()
	var count uint32
	result, _, err := getProcessHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&count)))
	if result == 0 {
		t.Fatal(err)
	}
	return count
}

func assertHandleCountReturns(t *testing.T, want uint32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processHandleCount(t) <= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("handle count=%d want<=%d", processHandleCount(t), want)
}
