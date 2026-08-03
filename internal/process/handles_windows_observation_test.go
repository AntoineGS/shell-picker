//go:build windows

package process

import (
	"context"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsObservationStartsOnlyAfterJobAssignmentAndResume(t *testing.T) {
	oldAssign, oldResume := winAssignProcessToJobObject, winResumeThread
	defer func() {
		winAssignProcessToJobObject, winResumeThread = oldAssign, oldResume
	}()
	assigned, resumed := false, false
	winAssignProcessToJobObject = func(job, process windows.Handle) error {
		err := oldAssign(job, process)
		if err == nil {
			assigned = true
		}
		return err
	}
	winResumeThread = func(thread windows.Handle) (uint32, error) {
		if !assigned {
			t.Error("ResumeThread called before job assignment")
		}
		previous, err := oldResume(thread)
		if err == nil {
			resumed = true
		}
		return previous, err
	}
	var events []ProcessEvent
	child, err := (Runner{Observe: func(event ProcessEvent) {
		if event.Phase == "start" && (!assigned || !resumed) {
			t.Error("start observed before assignment and resume completed")
		}
		events = append(events, event)
	}}).Start(context.Background(), helperSpec("exit", "0"))
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if !assigned || !resumed || eventPhases(events) != "attempt,start,exit" {
		t.Fatalf("assigned=%v resumed=%v events=%+v", assigned, resumed, events)
	}
}

func TestWindowsAssignmentOrResumeFailureDoesNotObserveStart(t *testing.T) {
	for _, stage := range []winStage{stageAssignJob, stageResumeThread} {
		t.Run(stage.String(), func(t *testing.T) {
			var events []ProcessEvent
			err, childPID := startWithInjectedFailureObserved(stage, func(event ProcessEvent) {
				events = append(events, event)
			})
			if err == nil {
				t.Fatal("injected stage succeeded")
			}
			if childPID != 0 {
				assertProcessGoneWithin(t, childPID, 2*time.Second)
			}
			if got := eventPhases(events); got != "attempt" {
				t.Fatalf("failure stage=%s observed phases=%q events=%+v", stage, got, events)
			}
		})
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
