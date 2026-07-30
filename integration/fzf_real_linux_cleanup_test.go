//go:build linux

package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestLinuxTerminalSessionWaitIsRepeatableAndConcurrent(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestLinuxTerminalSessionHelperProcess")
	command.Env = append(os.Environ(), "SHELL_PICKER_TERMINAL_HELPER=exit")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	session := newLinuxCommandSessionForTest(command)
	var wait sync.WaitGroup
	errorsSeen := make([]error, 8)
	for index := range errorsSeen {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsSeen[index] = session.Wait(testContext(t))
		}()
	}
	wait.Wait()
	for _, err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Wait returned %v", err)
		}
	}
	if err := session.Wait(testContext(t)); err != nil {
		t.Fatalf("repeated Wait returned %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxTerminalSessionTimeoutThenConcurrentClose(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestLinuxTerminalSessionHelperProcess")
	command.Env = append(os.Environ(), "SHELL_PICKER_TERMINAL_HELPER=block")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	session := newLinuxCommandSessionForTest(command)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := session.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error=%v", err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := session.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
		}()
	}
	wait.Wait()
	if err := session.Wait(testContext(t)); err == nil {
		t.Fatal("Wait after forced Close succeeded")
	}
}

func TestStartLinuxTerminalCommandFailureClosesOwnedResources(t *testing.T) {
	drainDone, traceDone := make(chan struct{}), make(chan struct{})
	close(drainDone)
	close(traceDone)
	master, err := os.CreateTemp(t.TempDir(), "master")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := os.CreateTemp(t.TempDir(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	dummy, err := os.CreateTemp(t.TempDir(), "dummy")
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.CreateTemp(t.TempDir(), "slave")
	if err != nil {
		t.Fatal(err)
	}
	session := &linuxTerminalSession{master: master, traceReader: reader, dummyWriter: dummy,
		drainDone: drainDone, traceDone: traceDone, waitDone: make(chan struct{})}
	command := exec.Command(filepath.Join(t.TempDir(), "missing-picker"))

	if err := startLinuxTerminalCommand(session, command, slave); err == nil {
		t.Fatal("missing picker started")
	}
	for name, file := range map[string]*os.File{"master": master, "reader": reader, "dummy": dummy, "slave": slave} {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("%s remains open: %v", name, err)
		}
	}
}

func TestLinuxTerminalSessionHelperProcess(t *testing.T) {
	switch os.Getenv("SHELL_PICKER_TERMINAL_HELPER") {
	case "exit":
		return
	case "block":
		for {
			_ = syscall.Pause()
		}
	default:
		return
	}
}

func newLinuxCommandSessionForTest(command *exec.Cmd) *linuxTerminalSession {
	drainDone, traceDone := make(chan struct{}), make(chan struct{})
	close(drainDone)
	close(traceDone)
	session := &linuxTerminalSession{cmd: command, waitDone: make(chan struct{}), drainDone: drainDone, traceDone: traceDone}
	go session.waitCommand()
	return session
}
