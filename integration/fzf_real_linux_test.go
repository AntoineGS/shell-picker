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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

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

	outputMu sync.Mutex
	output   bytes.Buffer
	eventMu  sync.Mutex
	events   []traceEvent
	changed  chan struct{}

	waitMu    sync.Mutex
	waitErr   error
	waitDone  chan struct{}
	drainDone chan struct{}
	traceDone chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (session *linuxTerminalSession) TraceEvents() []traceEvent { return session.traceEvents() }

func (session *linuxTerminalSession) AssertProcessTopology(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	nodes := make(map[int]linuxProcessNode)
	for _, entry := range entries {
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil {
			continue
		}
		ppid, parentErr := linuxParentPID(pid)
		exe, exeErr := os.Readlink("/proc/" + entry.Name() + "/exe")
		if parentErr != nil || exeErr != nil {
			continue
		}
		raw, _ := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		nodes[pid] = linuxProcessNode{pid: pid, ppid: ppid, exe: exe,
			args: strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")}
	}
	descendants := map[int]bool{session.PID(): true}
	for changed := true; changed; {
		changed = false
		for pid, node := range nodes {
			if !descendants[pid] && descendants[node.ppid] {
				descendants[pid], changed = true, true
			}
		}
	}
	wantFZF, err := filepath.EvalSymlinks(session.fzfPath)
	if err != nil {
		t.Fatal(err)
	}
	var fzfPIDs []int
	for pid := range descendants {
		if pid != session.PID() && nodes[pid].exe == wantFZF {
			fzfPIDs = append(fzfPIDs, pid)
		}
	}
	if len(fzfPIDs) != 1 {
		t.Fatalf("fzf child count=%d want 1", len(fzfPIDs))
	}
	rawEnvironment, err := os.ReadFile("/proc/" + strconv.Itoa(fzfPIDs[0]) + "/environ")
	if err != nil {
		t.Fatalf("read owned fzf environment for pid %d: %v", fzfPIDs[0], err)
	}
	actualCredentials, err := parseControlledFZFEnvironment(rawEnvironment)
	if err != nil {
		t.Fatalf("validate owned fzf controlled environment for pid %d: %v", fzfPIDs[0], err)
	}
	for pid := range descendants {
		if pid == session.PID() {
			continue
		}
		node := nodes[pid]
		if node.exe == wantFZF {
			if node.ppid != session.PID() {
				t.Fatalf("fzf pid %d parent=%d want picker %d", pid, node.ppid, session.PID())
			}
		}
		base := strings.ToLower(filepath.Base(node.exe))
		if map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "cmd.exe": true, "powershell.exe": true}[base] {
			t.Fatalf("interpreter role in picker tree pid=%d", pid)
		}
		for _, argument := range node.args {
			if argument == "--listen" || strings.HasPrefix(argument, "--listen=") || strings.Contains(argument, "SHELL_PICKER_TOKEN") {
				t.Fatalf("listener or credential name in process argv pid=%d", pid)
			}
			for _, canary := range session.argvCanaries {
				if canary != "" && strings.Contains(argument, canary) {
					t.Fatalf("stale callback credential canary in process argv pid=%d", pid)
				}
			}
			for _, credential := range actualCredentials {
				if strings.Contains(argument, credential) {
					t.Fatalf("actual controlled callback credential in process argv pid=%d", pid)
				}
			}
		}
	}
}

func parseControlledFZFEnvironment(raw []byte) ([]string, error) {
	values := make(map[string]string, 2)
	for _, entry := range bytes.Split(raw, []byte{0}) {
		key, value, ok := bytes.Cut(entry, []byte{'='})
		if !ok {
			continue
		}
		name := string(key)
		if name != "SHELL_PICKER_ADDR" && name != "SHELL_PICKER_TOKEN" {
			continue
		}
		if len(value) == 0 {
			return nil, fmt.Errorf("empty %s", name)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("duplicate %s", name)
		}
		values[name] = string(value)
	}
	address, addressOK := values["SHELL_PICKER_ADDR"]
	token, tokenOK := values["SHELL_PICKER_TOKEN"]
	if !addressOK || !tokenOK {
		return nil, errors.New("missing controlled callback environment")
	}
	return []string{address, token}, nil
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
		drainDone: make(chan struct{}), traceDone: make(chan struct{})}
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
		_ = slave.Close()
		if session.master != nil {
			_ = session.master.Close()
		}
		if session.dummyWriter != nil {
			_ = session.dummyWriter.Close()
		}
		if session.traceReader != nil {
			_ = session.traceReader.Close()
		}
		<-session.drainDone
		<-session.traceDone
		return err
	}
	session.cmd = command
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

func (session *linuxTerminalSession) drainTrace() {
	defer close(session.traceDone)
	scanner := bufio.NewScanner(session.traceReader)
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

func (session *linuxTerminalSession) waitCommand() {
	err := session.cmd.Wait()
	session.waitMu.Lock()
	session.waitErr = err
	session.waitMu.Unlock()
	if session.dummyWriter != nil {
		_ = session.dummyWriter.Close()
	}
	close(session.waitDone)
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
				(wanted.Renderer == "" || event.Renderer == wanted.Renderer) &&
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
	session.closeOnce.Do(func() {
		if session.cmd != nil && session.cmd.Process != nil {
			select {
			case <-session.waitDone:
			default:
				_ = syscall.Kill(-session.cmd.Process.Pid, syscall.SIGKILL)
				_ = session.cmd.Process.Kill()
			}
		}
		if session.master != nil {
			_ = session.master.Close()
		}
		if session.dummyWriter != nil {
			_ = session.dummyWriter.Close()
		}
		if session.cmd != nil {
			<-session.waitDone
		}
		<-session.drainDone
		<-session.traceDone
		if session.traceReader != nil {
			_ = session.traceReader.Close()
		}
	})
	return session.closeErr
}
