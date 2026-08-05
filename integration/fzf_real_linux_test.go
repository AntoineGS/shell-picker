//go:build linux

package integration

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
	"golang.org/x/sys/unix"
)

type linuxTerminalSession struct {
	t            *testing.T
	master       *os.File
	traceReader  *os.File
	dummyWriter  *os.File
	cmd          *exec.Cmd
	fzfPath      string
	argvCanaries []string
	sidecar      bool
	recorder     *descendantRecorder

	outputMu      sync.Mutex
	output        bytes.Buffer
	firstOutputAt time.Time
	outputChanged chan struct{}
	eventMu       sync.Mutex
	events        []traceEvent
	changed       chan struct{}

	waitMu       sync.Mutex
	waitErr      error
	waitDone     chan struct{}
	drainDone    chan struct{}
	traceDone    chan struct{}
	closeMu      sync.Mutex
	closeAttempt chan struct{}
	closeRunning bool
	closed       bool
	closeErr     error
	stopMu       sync.Mutex
	stop         chan struct{}
	stopOnce     sync.Once

	cleanupTimeout       time.Duration
	masterCloseOnce      sync.Once
	traceReaderCloseOnce sync.Once
	dummyWriterCloseOnce sync.Once
}

func (session *linuxTerminalSession) TraceEvents() []traceEvent { return session.traceEvents() }

func (session *linuxTerminalSession) FirstOutputAt() time.Time {
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	return session.firstOutputAt
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
	readerFD, err := unix.Open(tracePath, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		t.Fatalf("open trace FIFO reader: %v", err)
	}
	dummyFD, err := unix.Open(tracePath, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(readerFD)
		_ = master.Close()
		_ = slave.Close()
		t.Fatalf("open trace FIFO dummy writer: %v", err)
	}
	if err := unix.SetNonblock(readerFD, false); err != nil {
		_ = unix.Close(readerFD)
		_ = unix.Close(dummyFD)
		_ = master.Close()
		_ = slave.Close()
		t.Fatalf("set trace FIFO reader blocking: %v", err)
	}
	session := &linuxTerminalSession{t: t, master: master, traceReader: os.NewFile(uintptr(readerFD), tracePath),
		dummyWriter: os.NewFile(uintptr(dummyFD), tracePath), changed: make(chan struct{}), waitDone: make(chan struct{}),
		drainDone: make(chan struct{}), traceDone: make(chan struct{}), outputChanged: make(chan struct{}),
		sidecar: realFZFSidecarEnabled(config.Environment), recorder: newDescendantRecorder(snapshotDescendantProcessRecords)}
	for index, argument := range config.Args {
		if argument == "--fzf" && index+1 < len(config.Args) {
			session.fzfPath = config.Args[index+1]
		}
	}
	for _, entry := range config.Environment {
		if strings.HasPrefix(entry, "SHELL_PICKER_ADDR=") || strings.HasPrefix(entry, "SHELL_PICKER_TOKEN=") {
			if _, value, ok := strings.Cut(entry, "="); ok {
				session.argvCanaries = append(session.argvCanaries, value)
			}
		}
	}
	session.recorder.Start()
	go session.drainPTY()
	go session.drainTrace()

	args := append(append([]string(nil), config.Args...), "--trace", tracePath)
	command := exec.Command(config.Path, args...)
	command.Dir, command.Env = config.Directory, config.Environment
	command.Stdin, command.Stdout, command.Stderr = slave, slave, slave
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := startLinuxTerminalCommand(session, command, slave); err != nil {
		t.Fatalf("start picker in PTY: %v", err)
	}
	return session
}

func startLinuxTerminalCommand(session *linuxTerminalSession, command *exec.Cmd, slave *os.File) error {
	if err := command.Start(); err != nil {
		session.stopDescendantRecorder()
		_ = slave.Close()
		session.closeMaster()
		session.closeDummyWriter()
		session.closeTraceReader()
		cleanupTimeout := session.cleanupTimeout
		if cleanupTimeout <= 0 {
			cleanupTimeout = 5 * time.Second
		}
		deadline := time.Now().Add(cleanupTimeout)
		if !waitLinuxDoneUntil(session.drainDone, deadline) || !waitLinuxDoneUntil(session.traceDone, deadline) {
			return errors.Join(err, process.ErrWaitDelay)
		}
		return err
	}
	session.cmd = command
	session.recorder.SetRoot(command.Process.Pid)
	session.recorder.Capture()
	_ = slave.Close()
	go session.waitCommand()
	return nil
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
		if session.stopRequested() {
			return
		}
		n, err := session.master.Read(buffer)
		if n > 0 {
			session.outputMu.Lock()
			if session.firstOutputAt.IsZero() {
				session.firstOutputAt = time.Now()
			}
			_, _ = session.output.Write(buffer[:n])
			if session.outputChanged != nil {
				close(session.outputChanged)
			}
			session.outputChanged = make(chan struct{})
			session.outputMu.Unlock()
			session.captureDescendantSample()
		}
		if err != nil {
			return
		}
	}
}

func (session *linuxTerminalSession) drainTrace() {
	defer close(session.traceDone)
	if session.stopRequested() {
		return
	}
	scanner := bufio.NewScanner(session.traceReader)
	for scanner.Scan() {
		if session.stopRequested() {
			return
		}
		event, err := decodeTraceEvent(scanner.Bytes())
		if err != nil {
			session.publishTraceError(err)
			return
		}
		session.eventMu.Lock()
		session.events = append(session.events, event)
		close(session.changed)
		session.changed = make(chan struct{})
		session.eventMu.Unlock()
		session.captureDescendantSample()
	}
	if err := scanner.Err(); err != nil {
		session.publishTraceError(err)
	}
}

func (session *linuxTerminalSession) closeMaster() {
	session.masterCloseOnce.Do(func() {
		if session.master != nil {
			_ = session.master.Close()
		}
	})
}

func (session *linuxTerminalSession) closeTraceReader() {
	session.traceReaderCloseOnce.Do(func() {
		if session.traceReader != nil {
			_ = session.traceReader.Close()
		}
	})
}

func (session *linuxTerminalSession) closeDummyWriter() {
	session.dummyWriterCloseOnce.Do(func() {
		if session.dummyWriter != nil {
			_ = session.dummyWriter.Close()
		}
	})
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
			if matchesTraceBarrier(event, wanted) {
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
			events := session.traceEvents()
			session.t.Fatalf("wait for barrier %+v: %v; sidecar=%s; events=%+v; output=%q", wanted, ctx.Err(), sidecarDiagnostics(events), events, session.Output())
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

func (session *linuxTerminalSession) ResultBytes() []byte { return session.Output() }

func (session *linuxTerminalSession) WaitOutputAfter(ctx context.Context, before int) {
	session.t.Helper()
	for {
		session.outputMu.Lock()
		if session.output.Len() > before {
			session.outputMu.Unlock()
			return
		}
		if session.outputChanged == nil {
			session.outputChanged = make(chan struct{})
		}
		changed := session.outputChanged
		session.outputMu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			session.t.Fatalf("wait for terminal output after %d bytes: %v", before, ctx.Err())
		}
	}
}

func (session *linuxTerminalSession) CloseInput() error { return session.Send([]byte{0x04}) }
