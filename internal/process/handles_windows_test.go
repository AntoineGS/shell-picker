//go:build windows

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
			err, childPID, ownership, fail := startWithInjectedFailure(stage)
			ownership.assertFailure(t, stage, err, fail)
			ownership.assertClosed(t)
			if childPID != 0 {
				assertProcessGoneWithin(t, childPID, 2*time.Second)
			}
		})
	}
}

func TestHandleOwnershipRejectsInvalidTransitions(t *testing.T) {
	ownership := &testHandleOwnership{}
	ownership.register(windows.Handle(0x40), "first")
	ownership.close(windows.Handle(0x40), nil)
	ownership.register(windows.Handle(0x40), "second")
	ownership.close(windows.Handle(0x40), nil)
	ownership.close(windows.Handle(0x40), nil)
	ownership.close(windows.Handle(0x44), nil)
	ownership.register(windows.Handle(0x48), "parent")
	ownership.transfer(windows.Handle(0x48), "os.File")
	ownership.transfer(windows.Handle(0x48), "os.File")
	ownership.close(windows.Handle(0x48), nil)
	if got, want := len(ownership.violations), 4; got != want {
		t.Fatalf("invalid transitions=%d want=%d: %v", got, want, ownership.violations)
	}
}

type testHandleOwnership struct {
	lifetimes  []testHandleLifetime
	violations []error
	injected   []winStage
}

type testHandleLifetime struct {
	handle windows.Handle
	phase  string
	owner  string
	state  testHandleState
}

type testHandleState uint8

const (
	testHandleOpen testHandleState = iota
	testHandleTransferred
	testHandleClosed
)

func (ownership *testHandleOwnership) register(handle windows.Handle, phase string) {
	if handle == 0 {
		ownership.violations = append(ownership.violations, fmt.Errorf("%s returned zero handle", phase))
		return
	}
	if lifetime := ownership.latest(handle); lifetime != nil && lifetime.state != testHandleClosed {
		ownership.violations = append(ownership.violations,
			fmt.Errorf("%s reused live %s handle %#x", phase, lifetime.phase, handle))
		return
	}
	ownership.lifetimes = append(ownership.lifetimes, testHandleLifetime{handle: handle, phase: phase})
}

func (ownership *testHandleOwnership) close(handle windows.Handle, closeErr error) {
	lifetime := ownership.latest(handle)
	if lifetime == nil {
		ownership.violations = append(ownership.violations, fmt.Errorf("unknown close of handle %#x", handle))
		return
	}
	switch lifetime.state {
	case testHandleClosed:
		ownership.violations = append(ownership.violations, fmt.Errorf("duplicate close of %s handle %#x", lifetime.phase, handle))
	case testHandleTransferred:
		ownership.violations = append(ownership.violations,
			fmt.Errorf("close of transferred %s handle %#x owned by %s", lifetime.phase, handle, lifetime.owner))
	case testHandleOpen:
		if closeErr != nil {
			ownership.violations = append(ownership.violations,
				fmt.Errorf("close of %s handle %#x failed: %w", lifetime.phase, handle, closeErr))
			return
		}
		lifetime.state = testHandleClosed
	}
}

func (ownership *testHandleOwnership) transfer(handle windows.Handle, owner string) {
	lifetime := ownership.latest(handle)
	if lifetime == nil {
		ownership.violations = append(ownership.violations, fmt.Errorf("unknown transfer of handle %#x to %s", handle, owner))
		return
	}
	switch lifetime.state {
	case testHandleClosed:
		ownership.violations = append(ownership.violations,
			fmt.Errorf("transfer of closed %s handle %#x to %s", lifetime.phase, handle, owner))
	case testHandleTransferred:
		ownership.violations = append(ownership.violations,
			fmt.Errorf("duplicate transfer of %s handle %#x to %s", lifetime.phase, handle, owner))
	case testHandleOpen:
		lifetime.owner = owner
		lifetime.state = testHandleTransferred
	}
}

func (ownership *testHandleOwnership) markInjected(stage winStage) {
	ownership.injected = append(ownership.injected, stage)
}

func (ownership *testHandleOwnership) latest(handle windows.Handle) *testHandleLifetime {
	for index := len(ownership.lifetimes) - 1; index >= 0; index-- {
		if ownership.lifetimes[index].handle == handle {
			return &ownership.lifetimes[index]
		}
	}
	return nil
}

func (ownership *testHandleOwnership) assertFailure(t *testing.T, stage winStage, err, fail error) {
	t.Helper()
	if len(ownership.injected) != 1 || ownership.injected[0] != stage {
		t.Fatalf("stage %s injection calls=%v, want exactly one selected-stage call", stage, ownership.injected)
	}
	if !errors.Is(err, fail) {
		t.Fatalf("stage %s error=%v, want errors.Is(error, %v)", stage, err, fail)
	}
}

func (ownership *testHandleOwnership) assertClosed(t *testing.T) {
	t.Helper()
	for _, violation := range ownership.violations {
		t.Errorf("ownership transition: %v", violation)
	}
	for index := range ownership.lifetimes {
		lifetime := &ownership.lifetimes[index]
		switch lifetime.state {
		case testHandleOpen:
			t.Errorf("%s handle %#x remains owned", lifetime.phase, lifetime.handle)
		case testHandleTransferred:
			if err := assertWindowsHandleClosed(lifetime.handle); err != nil {
				t.Errorf("%s handle %#x transferred to %s was not closed: %v", lifetime.phase, lifetime.handle, lifetime.owner, err)
				continue
			}
			lifetime.state = testHandleClosed
		}
	}
}

// os.File.Close bypasses winCloseHandle, so transferred pipe ownership is probed after cleanup.
func assertWindowsHandleClosed(handle windows.Handle) error {
	if _, err := windows.GetFileType(handle); err == nil {
		return errors.New("handle is still open")
	} else if !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return fmt.Errorf("probe handle: %w", err)
	}
	return nil
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

func startWithInjectedFailure(stage winStage) (error, int, *testHandleOwnership, error) {
	fail := error(syscall.Errno(windows.ERROR_INVALID_FUNCTION))
	oldCreateFile, oldDuplicate, oldPipe, oldClose := winCreateFile, winDuplicateHandle, winCreatePipe, winCloseHandle
	oldSetHandle, oldCreateJob, oldSetJob := winSetHandleInformation, winCreateJobObject, winSetInformationJobObject
	oldAttributes, oldUpdate := winNewAttributeList, winUpdateHandleList
	oldCreateProcess, oldAssign, oldResume := winCreateProcess, winAssignProcessToJobObject, winResumeThread
	ownership := &testHandleOwnership{}
	childPID := 0
	defer func() {
		winCreateFile, winDuplicateHandle, winCreatePipe, winCloseHandle = oldCreateFile, oldDuplicate, oldPipe, oldClose
		winSetHandleInformation, winCreateJobObject, winSetInformationJobObject = oldSetHandle, oldCreateJob, oldSetJob
		winNewAttributeList, winUpdateHandleList = oldAttributes, oldUpdate
		winCreateProcess, winAssignProcessToJobObject, winResumeThread = oldCreateProcess, oldAssign, oldResume
	}()
	winCloseHandle = func(handle windows.Handle) error {
		err := oldClose(handle)
		ownership.close(handle, err)
		return err
	}
	winCreateFile = func(name *uint16, access, share uint32, sa *windows.SecurityAttributes, disposition, flags uint32,
		template windows.Handle) (windows.Handle, error) {
		handle, err := oldCreateFile(name, access, share, sa, disposition, flags, template)
		if err == nil {
			ownership.register(handle, "CreateFile")
		}
		return handle, err
	}
	winDuplicateHandle = func(sourceProcess, sourceHandle, targetProcess windows.Handle, target *windows.Handle,
		desiredAccess uint32, inherit bool, options uint32) error {
		err := oldDuplicate(sourceProcess, sourceHandle, targetProcess, target, desiredAccess, inherit, options)
		if err == nil {
			ownership.register(*target, "DuplicateHandle")
		}
		return err
	}
	winCreatePipe = func(read, write *windows.Handle, sa *windows.SecurityAttributes, size uint32) error {
		err := oldPipe(read, write, sa, size)
		if err == nil {
			ownership.register(*read, "CreatePipe read")
			ownership.register(*write, "CreatePipe write")
		}
		return err
	}
	winSetHandleInformation = func(handle windows.Handle, mask, flags uint32) error {
		err := oldSetHandle(handle, mask, flags)
		if err == nil {
			ownership.transfer(handle, "os.File")
		}
		return err
	}
	winCreateJobObject = func(sa *windows.SecurityAttributes, name *uint16) (windows.Handle, error) {
		handle, err := oldCreateJob(sa, name)
		if err == nil {
			ownership.register(handle, "CreateJobObject")
		}
		return handle, err
	}
	winCreateProcess = func(app, command *uint16, processSA, threadSA *windows.SecurityAttributes, inherit bool,
		flags uint32, environment, directory *uint16, startup *windows.StartupInfo, info *windows.ProcessInformation) error {
		err := oldCreateProcess(app, command, processSA, threadSA, inherit, flags, environment, directory, startup, info)
		if err == nil {
			ownership.register(info.Process, "CreateProcess process")
			ownership.register(info.Thread, "CreateProcess thread")
			childPID = int(info.ProcessId)
		}
		return err
	}
	switch stage {
	case stageDevNull:
		winCreateFile = func(*uint16, uint32, uint32, *windows.SecurityAttributes, uint32, uint32, windows.Handle) (windows.Handle, error) {
			ownership.markInjected(stage)
			return 0, fail
		}
	case stageDuplicate:
		winDuplicateHandle = func(windows.Handle, windows.Handle, windows.Handle, *windows.Handle, uint32, bool, uint32) error {
			ownership.markInjected(stage)
			return fail
		}
	case stagePipe:
		winCreatePipe = func(*windows.Handle, *windows.Handle, *windows.SecurityAttributes, uint32) error {
			ownership.markInjected(stage)
			return fail
		}
	case stagePipeInheritance:
		winSetHandleInformation = func(handle windows.Handle, _ uint32, _ uint32) error {
			ownership.markInjected(stage)
			return fail
		}
	case stageCreateJob:
		winCreateJobObject = func(*windows.SecurityAttributes, *uint16) (windows.Handle, error) {
			ownership.markInjected(stage)
			return 0, fail
		}
	case stageConfigureJob:
		winSetInformationJobObject = func(windows.Handle, uint32, uintptr, uint32) (int, error) {
			ownership.markInjected(stage)
			return 0, fail
		}
	case stageAttributeList:
		winNewAttributeList = func(uint32) (*windows.ProcThreadAttributeListContainer, error) {
			ownership.markInjected(stage)
			return nil, fail
		}
	case stageUpdateHandleList:
		winUpdateHandleList = func(*windows.ProcThreadAttributeListContainer, []windows.Handle) error {
			ownership.markInjected(stage)
			return fail
		}
	case stageCreateProcess:
		winCreateProcess = func(*uint16, *uint16, *windows.SecurityAttributes, *windows.SecurityAttributes, bool, uint32,
			*uint16, *uint16, *windows.StartupInfo, *windows.ProcessInformation) error {
			ownership.markInjected(stage)
			return fail
		}
	case stageAssignJob:
		winAssignProcessToJobObject = func(windows.Handle, windows.Handle) error {
			ownership.markInjected(stage)
			return fail
		}
	case stageResumeThread:
		winResumeThread = func(windows.Handle) (uint32, error) {
			ownership.markInjected(stage)
			return 0, fail
		}
	}
	spec := helperSpec("block")
	spec.Stdin, spec.Stdout, spec.Stderr = bytes.NewReader(nil), nil, &bytes.Buffer{}
	_, err := (Runner{}).Start(context.Background(), spec)
	return err, childPID, ownership, fail
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
