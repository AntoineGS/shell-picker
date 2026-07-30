//go:build linux

package integration

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPlatformPrerequisites(t *testing.T) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	defer unix.Close(fd)
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("TIOCSPTLCK: %v", err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("TIOCGPTN: %v", err)
	}
	slave, err := unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open devpts slave: %v", err)
	}
	_ = unix.Close(slave)
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatalf("pidfd_open: %v", err)
	}
	if err := unix.Close(pidfd); err != nil {
		t.Fatalf("close pidfd: %v", err)
	}
}
