//go:build windows

package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformHelper(args []string) bool {
	if args[0] == "retain-tree-descendant" {
		cmd := helperExec("block")
		if err := cmd.Start(); err != nil {
			return true
		}
		_, _ = fmt.Fprintln(os.Stdout, cmd.Process.Pid)
		return true
	}
	if args[0] == "handle-probe" {
		value, _ := strconv.ParseUint(args[1], 10, 64)
		_, err := windows.GetFileType(windows.Handle(value))
		_ = json.NewEncoder(os.Stdout).Encode(handleProbe{UnexpectedInheritedHandles: boolInt(err == nil)})
		return true
	}
	return false
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func assertProcessGoneWithin(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still exists", pid)
}

func processExists(pid int) bool {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	result, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && result == uint32(windows.WAIT_TIMEOUT)
}

func platformResourceCount(t *testing.T) uint64 { return uint64(processHandleCount(t)) }
func assertPlatformResourcesReturn(t *testing.T, want uint64) {
	assertHandleCountReturns(t, uint32(want))
}

func TestCreateProcessPassesStreamsAndArguments(t *testing.T) {
	spec := helperSpec("print-args", "a b", `x&y`)
	var out bytes.Buffer
	spec.Stdout = &out
	if err := (Runner{}).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if out.String() != "a b\x00x&y\x00" {
		t.Fatalf("output=%q", out.String())
	}
}

func TestWindowsRejectsExtraFilesBeforeProcessAttempt(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	attempted := false
	spec := helperSpec("exit", "0")
	spec.ExtraFiles = []*os.File{file}
	_, err = (Runner{Observe: func(ProcessEvent) { attempted = true }}).Start(context.Background(), spec)
	if err == nil || attempted {
		t.Fatalf("err=%v attempted=%v", err, attempted)
	}
}

func TestWindowsForegroundUsesOwnedJobWithoutTTY(t *testing.T) {
	spec := helperSpec("exit", "0")
	spec.Containment = ContainmentForegroundTree
	if err := (Runner{}).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedInheritedJobKillsDescendantAfterChildWait(t *testing.T) {
	spec := helperSpec("retain-tree-descendant")
	spec.Containment = ContainmentInheritTree
	var output bytes.Buffer
	spec.Stdout = &output
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := child.RetainTree()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(output.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.KillTree(); err != nil {
		t.Fatal(err)
	}
	if err := tree.KillTree(); err != nil {
		t.Fatalf("second KillTree: %v", err)
	}
	if err := tree.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tree.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	assertProcessGoneWithin(t, pid, 3*time.Second)
}

func TestWindowsExitErrorPreservesWaitDelayClassification(t *testing.T) {
	spec := helperSpec("hold-stdout-exit")
	spec.WaitDelay = 50 * time.Millisecond
	var output bytes.Buffer
	spec.Stdout = &output
	err := (Runner{}).Run(context.Background(), spec)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 || !errors.Is(err, ErrWaitDelay) {
		t.Fatalf("error=%v", err)
	}
	if err.Error() != exitErr.Error() {
		t.Fatalf("presentation=%q want %q", err, exitErr)
	}
}

type handleProbe struct {
	UnexpectedInheritedHandles int `json:"UnexpectedInheritedHandles"`
	PumpCount                  int `json:"-"`
}

func TestCreateProcessInheritsOnlyExplicitChildHandles(t *testing.T) {
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var sentinelR, sentinelW windows.Handle
	if err := windows.CreatePipe(&sentinelR, &sentinelW, sa, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(sentinelR)
	defer windows.CloseHandle(sentinelW)
	spec := helperSpec("handle-probe", fmt.Sprint(uintptr(sentinelR)))
	var output bytes.Buffer
	spec.Stdout, spec.Stderr = &output, &bytes.Buffer{}
	child, err := (Runner{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	result := handleProbe{PumpCount: child.pumpCount()}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.UnexpectedInheritedHandles != 0 || result.PumpCount != 2 {
		t.Fatalf("result=%+v", result)
	}
}

func TestSharedStdoutStderrWriterIsSerialized(t *testing.T) {
	writer := &concurrencyDetectingWriter{}
	spec := helperSpec("both-streams")
	spec.Stdout, spec.Stderr = writer, writer
	if err := (Runner{}).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if writer.concurrent.Load() != 0 {
		t.Fatal("stdout and stderr pumps wrote concurrently")
	}
}

type concurrencyDetectingWriter struct{ active, concurrent atomic.Int32 }

func (w *concurrencyDetectingWriter) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.concurrent.Add(1)
	}
	for i := 0; i < 10000; i++ {
	}
	w.active.Add(-1)
	return len(p), nil
}

func TestSanitizeEnvWindowsCaseInsensitiveAndLastWins(t *testing.T) {
	got := SanitizeEnv([]string{"Path=first", "PATH=last", "fzf_default_opts=bad", "shell_picker_x=bad"},
		map[string]string{"SHELL_PICKER_X": "good"})
	if len(got) != 2 || got[0] != "PATH=last" || got[1] != "SHELL_PICKER_X=good" {
		t.Fatalf("env=%q", got)
	}
}

func TestSanitizeEnvWindowsDeduplicatesControlledKeys(t *testing.T) {
	got := SanitizeEnv(nil, map[string]string{"Path": "first", "PATH": "last", "PATO": "middle"})
	if len(got) != 2 || got[0] != "Path=first" || got[1] != "PATO=middle" {
		t.Fatalf("controlled env=%q", got)
	}
}
