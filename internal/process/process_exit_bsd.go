//go:build darwin || freebsd

package process

import (
	"errors"
	"syscall"
)

func nonReapingExitSupported() bool { return true }

func observeProcessExit(pid int) error {
	queue, err := syscall.Kqueue()
	if err != nil {
		return err
	}
	defer syscall.Close(queue)
	change := syscall.Kevent_t{Ident: uint64(pid), Filter: syscall.EVFILT_PROC,
		Flags: syscall.EV_ADD | syscall.EV_ONESHOT, Fflags: syscall.NOTE_EXIT}
	events := make([]syscall.Kevent_t, 1)
	for {
		_, err = syscall.Kevent(queue, []syscall.Kevent_t{change}, events, nil)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
