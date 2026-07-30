//go:build linux

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

type linuxTerminalSession struct {
	t      *testing.T
	master *os.File
	cmd    *exec.Cmd

	outputMu sync.Mutex
	output   bytes.Buffer
	eventMu  sync.Mutex
	events   []traceEvent
	changed  chan struct{}

	waitResult chan error
	drainDone  chan struct{}
	traceDone  chan struct{}
	closeOnce  sync.Once
	closeErr   error
}

func newTerminalSession(t *testing.T, config terminalConfig) terminalSession {
	t.Helper()
	master, slave := openLinuxPTY(t, config.Columns, config.Lines)
	tracePath := filepathInTemp(t, "trace.fifo")
	if err := unix.Mkfifo(tracePath, 0o600); err != nil {
		master.Close()
		slave.Close()
		t.Fatalf("create trace FIFO: %v", err)
	}
	session := &linuxTerminalSession{t: t, master: master, changed: make(chan struct{}),
		waitResult: make(chan error, 1), drainDone: make(chan struct{}), traceDone: make(chan struct{})}
	go session.drainPTY()
	readerReady := make(chan struct{})
	go session.drainTrace(tracePath, readerReady)
	<-readerReady

	args := append(append([]string(nil), config.Args...), "--trace", tracePath)
	command := exec.Command(config.Path, args...)
	command.Dir, command.Env = config.Directory, config.Environment
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		t.Fatalf("start picker in PTY: %v", err)
	}
	session.cmd = command
	_ = slave.Close()
	go func() { session.waitResult <- command.Wait() }()
	return session
}

func openLinuxPTY(t *testing.T, columns, lines uint16) (*os.File, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("real-fzf PTY prerequisite /dev/ptmx unavailable: %v", err)
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	fail := func(format string, args ...any) {
		_ = master.Close()
		t.Skipf(format, args...)
	}
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		fail("real-fzf PTY prerequisite TIOCSPTLCK unavailable: %v", err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		fail("real-fzf PTY prerequisite TIOCGPTN unavailable: %v", err)
	}
	slaveFD, err := unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		t.Skipf("real-fzf devpts slave unavailable: %v", err)
	}
	slave := os.NewFile(uintptr(slaveFD), fmt.Sprintf("/dev/pts/%d", number))
	if err := unix.IoctlSetWinsize(slaveFD, unix.TIOCSWINSZ, &unix.Winsize{Col: columns, Row: lines}); err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Skipf("real-fzf PTY resize prerequisite unavailable: %v", err)
	}
	if pidfd, err := unix.PidfdOpen(os.Getpid(), 0); err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Skipf("real-fzf pidfd prerequisite unavailable: %v", err)
	} else {
		_ = unix.Close(pidfd)
	}
	return master, slave
}

func filepathInTemp(t *testing.T, name string) string {
	t.Helper()
	return t.TempDir() + string(os.PathSeparator) + name
}

func (session *linuxTerminalSession) drainPTY() {
	defer close(session.drainDone)
	buffer := make([]byte, 32<<10)
	for {
		n, err := session.master.Read(buffer)
		if n > 0 {
			session.outputMu.Lock()
			_, _ = session.output.Write(buffer[:n])
			session.outputMu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (session *linuxTerminalSession) drainTrace(path string, ready chan<- struct{}) {
	defer close(session.traceDone)
	close(ready)
	file, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		session.publishTraceError(err)
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event traceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			session.publishTraceError(err)
			return
		}
		session.eventMu.Lock()
		session.events = append(session.events, event)
		close(session.changed)
		session.changed = make(chan struct{})
		session.eventMu.Unlock()
	}
	if err := scanner.Err(); err != nil {
		session.publishTraceError(err)
	}
}

func (session *linuxTerminalSession) publishTraceError(err error) {
	session.eventMu.Lock()
	session.events = append(session.events, traceEvent{Event: "trace.error", Outcome: err.Error()})
	close(session.changed)
	session.changed = make(chan struct{})
	session.eventMu.Unlock()
}

func (session *linuxTerminalSession) Send(data []byte) error {
	for len(data) > 0 {
		written, err := session.master.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (session *linuxTerminalSession) Resize(columns, lines uint16) error {
	if columns == 0 || lines == 0 {
		return errors.New("terminal resize dimensions must be nonzero")
	}
	return unix.IoctlSetWinsize(int(session.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: columns, Row: lines})
}

func (session *linuxTerminalSession) WaitBarrier(ctx context.Context, wanted barrier) traceEvent {
	session.t.Helper()
	if wanted.Count <= 0 {
		wanted.Count = 1
	}
	for {
		session.eventMu.Lock()
		count := 0
		var matched traceEvent
		for _, event := range session.events {
			if event.Event == "trace.error" {
				session.eventMu.Unlock()
				session.t.Fatalf("trace reader failed: %s", event.Outcome)
			}
			if event.Event == wanted.Event && (wanted.Operation == "" || event.Outcome == wanted.Operation) &&
				(wanted.Generation == 0 || event.Generation == wanted.Generation) {
				count++
				matched = event
				if count >= wanted.Count {
					session.eventMu.Unlock()
					return matched
				}
			}
		}
		changed := session.changed
		session.eventMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			session.t.Fatalf("wait for barrier %+v: %v; events=%+v; output=%q", wanted, ctx.Err(), session.traceEvents(), session.Output())
		}
	}
}

func (session *linuxTerminalSession) traceEvents() []traceEvent {
	session.eventMu.Lock()
	defer session.eventMu.Unlock()
	return append([]traceEvent(nil), session.events...)
}

func (session *linuxTerminalSession) PID() int { return session.cmd.Process.Pid }

func (session *linuxTerminalSession) Output() []byte {
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	return bytes.Clone(session.output.Bytes())
}

func (session *linuxTerminalSession) CloseInput() error { return session.Send([]byte{0x04}) }

func (session *linuxTerminalSession) Wait(ctx context.Context) error {
	select {
	case err := <-session.waitResult:
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
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (session *linuxTerminalSession) Close() error {
	session.closeOnce.Do(func() {
		if session.cmd != nil && session.cmd.Process != nil {
			_ = syscall.Kill(-session.cmd.Process.Pid, syscall.SIGKILL)
			_ = session.cmd.Process.Kill()
		}
		_ = session.master.Close()
		select {
		case <-session.drainDone:
		default:
		}
	})
	return session.closeErr
}
