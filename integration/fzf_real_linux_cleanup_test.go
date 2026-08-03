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

	"github.com/AntoineGS/shell-picker/internal/process"
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

func TestLinuxTerminalSessionCloseRetriesAfterWorkerTimeout(t *testing.T) {
	workerDone := make(chan struct{})
	waitDone := make(chan struct{})
	traceDone := make(chan struct{})
	close(waitDone)
	close(traceDone)
	session := &linuxTerminalSession{
		cleanupTimeout: 10 * time.Millisecond,
		waitDone:       waitDone,
		drainDone:      workerDone,
		traceDone:      traceDone,
	}

	if err := session.Close(); !errors.Is(err, process.ErrWaitDelay) {
		t.Fatalf("first Close=%v, want process.ErrWaitDelay", err)
	}
	close(workerDone)
	if err := session.Close(); err != nil {
		t.Fatalf("second Close=%v, want cleanup retry success", err)
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

func (session *linuxTerminalSession) waitCommand() {
	err := session.cmd.Wait()
	session.waitMu.Lock()
	session.waitErr = err
	session.waitMu.Unlock()
	session.closeDummyWriter()
	close(session.waitDone)
}

func (session *linuxTerminalSession) Wait(ctx context.Context) error {
	select {
	case <-session.waitDone:
		select {
		case <-session.drainDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-session.traceDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		session.waitMu.Lock()
		err := session.waitErr
		session.waitMu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *linuxTerminalSession) Close() error {
	session.closeMu.Lock()
	if session.closed {
		err := session.closeErr
		session.closeMu.Unlock()
		return err
	}
	if session.closeRunning {
		attempt := session.closeAttempt
		session.closeMu.Unlock()
		<-attempt
		session.closeMu.Lock()
		err := session.closeErr
		session.closeMu.Unlock()
		return err
	}
	session.closeRunning = true
	session.closeAttempt = make(chan struct{})
	attempt := session.closeAttempt
	session.closeMu.Unlock()

	err := session.closeAttemptRun()

	session.closeMu.Lock()
	session.closeErr = err
	session.closeRunning = false
	if session.resourcesReleased() {
		session.closed = true
	}
	close(attempt)
	session.closeMu.Unlock()
	return err
}

func (session *linuxTerminalSession) closeAttemptRun() error {
	session.requestStop()
	if session.cmd != nil && session.cmd.Process != nil {
		select {
		case <-session.waitDone:
		default:
			_ = syscall.Kill(-session.cmd.Process.Pid, syscall.SIGKILL)
			_ = session.cmd.Process.Kill()
		}
	}
	session.closeMaster()
	session.closeDummyWriter()
	session.closeTraceReader()
	timeout := session.cleanupTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var err error
	if session.cmd != nil && !waitLinuxDoneUntil(session.waitDone, deadline) {
		err = errors.Join(err, process.ErrWaitDelay)
	}
	if !waitLinuxDoneUntil(session.drainDone, deadline) {
		err = errors.Join(err, process.ErrWaitDelay)
	}
	if !waitLinuxDoneUntil(session.traceDone, deadline) {
		err = errors.Join(err, process.ErrWaitDelay)
	}
	return err
}

func (session *linuxTerminalSession) ensureStop() {
	session.stopMu.Lock()
	if session.stop == nil {
		session.stop = make(chan struct{})
	}
	session.stopMu.Unlock()
}

func (session *linuxTerminalSession) requestStop() {
	session.ensureStop()
	session.stopMu.Lock()
	stop := session.stop
	session.stopMu.Unlock()
	session.stopOnce.Do(func() { close(stop) })
}

func (session *linuxTerminalSession) stopRequested() bool {
	session.stopMu.Lock()
	stop := session.stop
	session.stopMu.Unlock()
	if stop == nil {
		return false
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func (session *linuxTerminalSession) resourcesReleased() bool {
	return (session.cmd == nil || linuxChannelClosed(session.waitDone)) &&
		linuxChannelClosed(session.drainDone) && linuxChannelClosed(session.traceDone)
}

func linuxChannelClosed(done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func waitLinuxDoneUntil(done <-chan struct{}, deadline time.Time) bool {
	if done == nil {
		return true
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
