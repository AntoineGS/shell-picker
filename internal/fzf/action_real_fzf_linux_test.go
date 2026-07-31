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
)

type terminalSnapshot struct {
	data []byte
	err  error
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
	go func() {
		var output bytes.Buffer
		buffer := make([]byte, 4096)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				_, _ = output.Write(buffer[:n])
				copy := bytes.Clone(output.Bytes())
				publishLatest(updates, terminalSnapshot{data: copy})
			}
			if err != nil {
				publishLatest(updates, terminalSnapshot{data: bytes.Clone(output.Bytes()), err: err})
				return
			}
		}
	}()

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
