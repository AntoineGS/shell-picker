//go:build linux

package integration

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func runPickerDocCommand(command *exec.Cmd) ([]byte, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open docs PTY: %w", err)
	}
	master := os.NewFile(uintptr(fd), "/dev/ptmx")
	defer master.Close()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return nil, fmt.Errorf("unlock docs PTY: %w", err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return nil, fmt.Errorf("identify docs PTY: %w", err)
	}
	slaveFD, err := unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open docs PTY slave: %w", err)
	}
	slave := os.NewFile(uintptr(slaveFD), fmt.Sprintf("/dev/pts/%d", number))
	var stdout, stderr bytes.Buffer
	command.Stdin, command.Stdout, command.Stderr = slave, &stdout, &stderr
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := command.Start(); err != nil {
		_ = slave.Close()
		return nil, err
	}
	_ = slave.Close()
	var terminalOutput bytes.Buffer
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&terminalOutput, master)
		close(drained)
	}()
	err = command.Wait()
	<-drained
	if err != nil {
		stdout.Write(stderr.Bytes())
		stdout.Write(terminalOutput.Bytes())
	}
	return stdout.Bytes(), err
}
