package fzf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type terminalSnapshot struct {
	data []byte
	err  error
}

// TestInstalledFZFFullOptionsDynamicKeyActionsAreParseable catches bundled
// unbind/rebind actions becoming invalid when Normal printable keys include
// literal parentheses.
func TestInstalledFZFFullOptionsDynamicKeyActionsAreParseable(t *testing.T) {
	path := os.Getenv("SHELL_PICKER_REAL_FZF")
	if path == "" {
		t.Skip("SHELL_PICKER_REAL_FZF is required for the installed full-options parse gate")
	}
	if err := CheckVersion(context.Background(), processpkg.Runner{}, path); err != nil {
		t.Fatal(err)
	}

	generated, err := Options(OptionsConfig{Picker: protocol.PickerCD, Prompt: "[I] ", Header: "/work/"})
	if err != nil {
		t.Fatal(err)
	}
	args := make([]string, 0, len(generated)+1)
	for _, option := range generated {
		if strings.HasPrefix(option, "--bind=") {
			args = append(args, option)
		}
	}
	args = append(args, "--filter=candidate")
	cmd := exec.Command(path, args...)
	cmd.Env = processpkg.SanitizeEnv(os.Environ(), map[string]string{"TERM": "xterm-256color"})
	cmd.Stdin = strings.NewReader("candidate\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fzf rejected complete generated options: %v; output=%q", err, output)
	}
}

func TestInstalledFZFActionSemantics(t *testing.T) {
	path := os.Getenv("SHELL_PICKER_REAL_FZF")
	if path == "" {
		t.Skip("SHELL_PICKER_REAL_FZF is required for the installed action-semantics gate")
	}
	if err := CheckVersion(context.Background(), processpkg.Runner{}, path); err != nil {
		t.Fatal(err)
	}

	headers := []string{
		`[windows] C:\`,
		`[unix] escaped\\name end`,
		`[close-plus] x)+execute(id) end`,
		`[comma-colon] x,y:z end`,
		`[substitution] {q} $(id) end`,
		`[actions] transform(e:en)+abort end`,
	}
	for _, header := range headers {
		t.Run(header, func(t *testing.T) {
			action, err := changeHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			screen := runFZFUntilText(t, path, action.text, header)
			if !bytes.Contains(screen, []byte(header)) {
				t.Fatalf("header %q not rendered unchanged; terminal output=%q", header, screen)
			}
		})
	}
}

func TestInstalledFZFInvalidPathRestoreArmingOrder(t *testing.T) {
	path := os.Getenv("SHELL_PICKER_REAL_FZF")
	if path == "" {
		t.Skip("SHELL_PICKER_REAL_FZF is required for the installed invalid-path ordering gate")
	}
	if err := CheckVersion(context.Background(), processpkg.Runner{}, path); err != nil {
		t.Fatal(err)
	}

	logPath := t.TempDir() + "/restore.log"
	tracePath := t.TempDir() + "/shell.log"
	shellPath := t.TempDir() + "/fzf-shell"
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$TRACE_LOG"
case "${1-}" in
	l:empty) printf 'reloaded\n' ;;
	p:invalid) printf 'invalid preview\n' ;;
	e:rs) printf 'restore\n' >>"$RESTORE_LOG" ;;
	*) exit 64 ;;
esac
`
	if err := os.WriteFile(shellPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	invalid, err := RenderEffect(protocol.Effect{Put: "/", InvalidPath: true})
	if err != nil {
		t.Fatal(err)
	}
	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()
	cmd := exec.Command(path,
		"--style=minimal", "--no-info", "--no-scrollbar", "--no-mouse", "--layout=reverse",
		"--with-shell="+shellPath,
		binding("change", transformEvent(protocol.OpRestoreView)),
		binding("result-final", keyAction("rebind", []string{"change"}), keyAction("unbind", []string{"result-final"})),
		binding("start", keyAction("unbind", []string{"change"}), action{text: invalid}),
		binding("esc", abort()),
	)
	cmd.Env = processpkg.SanitizeEnv(os.Environ(), map[string]string{
		"RESTORE_LOG": logPath,
		"TRACE_LOG":   tracePath,
		"TERM":        "xterm-256color",
	})
	cmd.Stdin = strings.NewReader("candidate\n")
	cmd.Stdout, cmd.Stderr = slave, slave
	cmd.ExtraFiles = []*os.File{slave}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 3}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	updates := make(chan terminalSnapshot, 8)
	go readTerminalSnapshots(master, updates)
	waitForTerminalText(t, updates, "invalid preview", tracePath)
	if got := restoreCount(t, logPath); got != 0 {
		t.Fatalf("invalid action fired change %d times before a query edit", got)
	}
	if _, err := master.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	waitForRestoreCount(t, logPath, 1)
	if _, err := master.Write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 130 {
			t.Fatalf("fzf exit: %v", err)
		}
	}
	if got := restoreCount(t, logPath); got != 1 {
		t.Fatalf("one query edit fired change %d times, want exactly once", got)
	}
}

func readTerminalSnapshots(master *os.File, updates chan terminalSnapshot) {
	var output bytes.Buffer
	buffer := make([]byte, 4096)
	for {
		n, err := master.Read(buffer)
		if n > 0 {
			_, _ = output.Write(buffer[:n])
			publishLatest(updates, terminalSnapshot{data: bytes.Clone(output.Bytes())})
		}
		if err != nil {
			publishLatest(updates, terminalSnapshot{data: bytes.Clone(output.Bytes()), err: err})
			return
		}
	}
}

func waitForTerminalText(t *testing.T, updates chan terminalSnapshot, text, tracePath string) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	var latest []byte
	for {
		select {
		case update := <-updates:
			latest = update.data
			if bytes.Contains(update.data, []byte(text)) {
				return
			}
			if update.err != nil && !errors.Is(update.err, os.ErrClosed) {
				t.Fatalf("fzf exited before rendering %q: %v; output=%q", text, update.err, update.data)
			}
		case <-timer.C:
			trace, _ := os.ReadFile(tracePath)
			t.Fatalf("timed out waiting for terminal text %q; shell calls=%q output=%q", text, trace, latest)
		}
	}
}

func waitForRestoreCount(t *testing.T, path string, want int) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			if got := restoreCount(t, path); got >= want {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %d restore callbacks; got %d", want, restoreCount(t, path))
		}
	}
}

func restoreCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(data, []byte("restore\n"))
}

func runFZFUntilText(t *testing.T, path, actionText, text string) []byte {
	t.Helper()
	target := []byte(text)
	master, slave := openTestPTY(t)
	defer master.Close()
	defer slave.Close()

	cmd := exec.Command(path,
		"--style=minimal", "--no-info", "--no-scrollbar", "--no-mouse", "--layout=reverse",
		"--bind=start:"+actionText,
	)
	cmd.Env = processpkg.SanitizeEnv(os.Environ(), map[string]string{"TERM": "xterm-256color"})
	cmd.Stdin = strings.NewReader("candidate\n")
	cmd.Stdout, cmd.Stderr = slave, slave
	cmd.ExtraFiles = []*os.File{slave}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 3}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	updates := make(chan terminalSnapshot, 8)
	go readTerminalSnapshots(master, updates)

	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	var latest []byte
	for {
		select {
		case update := <-updates:
			latest = update.data
			if bytes.Contains(latest, target) {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				return latest
			}
			if update.err != nil && !errors.Is(update.err, os.ErrClosed) {
				t.Fatalf("fzf exited before rendering text %q: %v; output=%q", text, update.err, latest)
			}
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			t.Fatalf("timed out waiting for text %q; output=%q", text, latest)
		}
	}
}

func publishLatest(updates chan terminalSnapshot, update terminalSnapshot) {
	select {
	case updates <- update:
		return
	default:
	}
	select {
	case <-updates:
	default:
	}
	updates <- update
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	unlocked := int32(0)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x40045431, uintptr(unsafe.Pointer(&unlocked))); errno != 0 {
		master.Close()
		t.Fatal(errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), 0x80045430, uintptr(unsafe.Pointer(&number))); errno != 0 {
		master.Close()
		t.Fatal(errno)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatal(err)
	}
	winsize := struct{ row, col, xpixel, ypixel uint16 }{row: 24, col: 160}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&winsize))); errno != 0 {
		master.Close()
		slave.Close()
		t.Fatal(errno)
	}
	return master, slave
}
