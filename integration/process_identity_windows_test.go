//go:build windows

package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

type windowsProcessIdentity struct {
	pid       int
	handle    windows.Handle
	closeOnce sync.Once
	closeErr  error
}

var task20OwnedHandleRegistry = struct {
	sync.Mutex
	handles map[windows.Handle]string
}{handles: make(map[windows.Handle]string)}

func registerTask20OwnedHandle(handle windows.Handle, metadata string) error {
	task20OwnedHandleRegistry.Lock()
	defer task20OwnedHandleRegistry.Unlock()
	if previous, exists := task20OwnedHandleRegistry.handles[handle]; exists {
		return fmt.Errorf("Task20 handle %#x already registered as %s", uintptr(handle), previous)
	}
	task20OwnedHandleRegistry.handles[handle] = metadata
	return nil
}

func closeTask20OwnedHandle(handle windows.Handle) error {
	err := windows.CloseHandle(handle)
	if err == nil {
		task20OwnedHandleRegistry.Lock()
		delete(task20OwnedHandleRegistry.handles, handle)
		task20OwnedHandleRegistry.Unlock()
	}
	return err
}

func snapshotTask20OwnedHandles() map[windows.Handle]string {
	task20OwnedHandleRegistry.Lock()
	defer task20OwnedHandleRegistry.Unlock()
	snapshot := make(map[windows.Handle]string, len(task20OwnedHandleRegistry.handles))
	for handle, kind := range task20OwnedHandleRegistry.handles {
		snapshot[handle] = kind
	}
	return snapshot
}

func task20ClassifiedResourceForHandle(handle windows.Handle) (task20ResourceIdentity, error) {
	classified, err := task20CurrentProcessClassifiedHandles()
	if err != nil {
		return task20ResourceIdentity{}, err
	}
	for identity, resource := range classified {
		if identity.Value == uintptr(handle) {
			return resource, nil
		}
	}
	return task20ResourceIdentity{}, fmt.Errorf("handle %#x was absent from the current-process classified snapshot", uintptr(handle))
}

func openOwnedProcessIdentity(pid int) (ownedProcessIdentity, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	resource, err := task20ClassifiedResourceForHandle(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("classify process identity handle %#x: %w", uintptr(handle), err)
	}
	if resource.Kind != task20HandleProcess {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("process identity handle %#x classified as %s", uintptr(handle), task20ResourceDiagnostic(resource))
	}
	if err := registerTask20OwnedHandle(handle, task20HandleRegistryMetadata("process/job", resource)); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return &windowsProcessIdentity{pid: pid, handle: handle}, nil
}

func openWindowsProductionProcessIdentity(pid int) (ownedProcessIdentity, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	return &windowsProcessIdentity{pid: pid, handle: handle}, nil
}

func verifyOwnedProcessGroup(int) error { return nil }

func (identity *windowsProcessIdentity) PID() int { return identity.pid }
func (identity *windowsProcessIdentity) Close() error {
	identity.closeOnce.Do(func() { identity.closeErr = closeTask20OwnedHandle(identity.handle) })
	return identity.closeErr
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
