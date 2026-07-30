//go:build !linux && !windows

package integration

import (
	"errors"
	"syscall"
)

func openOwnedProcessIdentity(int) (ownedProcessIdentity, error) {
	return nil, errors.New("process identity observer unavailable on this Unix target")
}

func verifyOwnedProcessGroup(pid int) error {
	group, err := syscall.Getpgid(pid)
	if err != nil {
		return err
	}
	if group != pid {
		return errors.New("owned process group does not match leader identity")
	}
	return nil
}
