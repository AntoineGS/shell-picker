//go:build windows

package integration

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

type windowsProcessIdentity struct {
	pid    int
	handle windows.Handle
}

func openOwnedProcessIdentity(pid int) (ownedProcessIdentity, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	return &windowsProcessIdentity{pid: pid, handle: handle}, nil
}

func verifyOwnedProcessGroup(int) error { return nil }

func (identity *windowsProcessIdentity) PID() int { return identity.pid }
func (identity *windowsProcessIdentity) Close() error {
	return windows.CloseHandle(identity.handle)
}
func (identity *windowsProcessIdentity) WaitGone(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("process handle wait requires deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	status, err := windows.WaitForSingleObject(identity.handle, uint32(remaining/time.Millisecond))
	if err != nil {
		return err
	}
	if status != windows.WAIT_OBJECT_0 {
		return context.DeadlineExceeded
	}
	return nil
}
