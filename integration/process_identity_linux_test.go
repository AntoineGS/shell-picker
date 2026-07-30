//go:build linux

package integration

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

type linuxProcessIdentity struct {
	pid int
	fd  int
}

func openOwnedProcessIdentity(pid int) (ownedProcessIdentity, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, err
	}
	return &linuxProcessIdentity{pid: pid, fd: fd}, nil
}

func verifyOwnedProcessGroup(pid int) error {
	group, err := unix.Getpgid(pid)
	if err != nil {
		return err
	}
	if group != pid {
		return errors.New("owned process group does not match leader identity")
	}
	return nil
}

func (identity *linuxProcessIdentity) PID() int { return identity.pid }
func (identity *linuxProcessIdentity) Close() error {
	return unix.Close(identity.fd)
}
func (identity *linuxProcessIdentity) WaitGone(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("pidfd wait requires deadline")
	}
	poll := []unix.PollFd{{Fd: int32(identity.fd), Events: unix.POLLIN}}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		timeout := unix.NsecToTimespec(remaining.Nanoseconds())
		_, err := unix.Ppoll(poll, &timeout, nil)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return err
		}
		if poll[0].Revents != 0 {
			return nil
		}
	}
}
