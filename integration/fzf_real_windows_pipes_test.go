//go:build windows

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func finalizeWindowsTerminalLaunch(session *windowsTerminalSession, config terminalConfig, ctx context.Context, deps windowsProductionDiscoveryDeps) error {
	session.handleMu.Lock()
	waitHandle := session.waitHandle
	session.handleMu.Unlock()
	if waitHandle == 0 {
		return errors.New("missing session-owned wait handle")
	}
	recordingRoot := session.pid
	if config.ProductionRootPath != "" {
		productionRoot, err := discoverWindowsProductionRoot(ctx, waitHandle, uint32(session.pid), config.ProductionRootPath, deps)
		if err != nil {
			return err
		}
		session.productionPID = productionRoot.pid
		session.productionMarker = productionRoot.marker
		session.productionIdentity = productionRoot.identity
		recordingRoot = productionRoot.pid
	}
	if session.recorder != nil {
		session.recorder.snapshot = func(int) ([]descendantProcessRecord, error) {
			return snapshotDescendantProcessRecordsForIdentity(session.processTreeRoot())
		}
	}
	session.recorder.SetRoot(recordingRoot)
	session.recorder.Capture()
	session.waitStarted = true
	session.resultStarted = true
	session.handleMu.Lock()
	waitHandle = session.waitHandle
	session.waitHandle = 0
	session.handleMu.Unlock()
	go session.waitProcess(waitHandle)
	go session.drainResult(session.result, session.resultDone)
	return nil
}

func (session *windowsTerminalSession) FirstOutputAt() time.Time {
	session.outputMu.Lock()
	defer session.outputMu.Unlock()
	return session.firstOutputAt
}

func createWindowsOverlappedReadPipe() (windows.Handle, windows.Handle, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return 0, 0, err
	}
	name := `\\.\pipe\shell-picker-conpty-output-` + hex.EncodeToString(raw)
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, 0, err
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		return 0, 0, err
	}
	server, err := windows.CreateNamedPipe(wide, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_FIRST_PIPE_INSTANCE|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 64<<10, 64<<10, 0, security)
	if err != nil {
		return 0, 0, err
	}
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	defer windows.CloseHandle(event)
	overlapped := windows.Overlapped{HEvent: event}
	err = windows.ConnectNamedPipe(server, &overlapped)
	connectPending := errors.Is(err, windows.ERROR_IO_PENDING)
	if err != nil && !errors.Is(err, windows.ERROR_IO_PENDING) && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	client, err := windows.CreateFile(wide, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		_ = windows.CancelIoEx(server, &overlapped)
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	if connectPending {
		var transferred uint32
		err = windows.GetOverlappedResult(server, &overlapped, &transferred, true)
	}
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(client)
		_ = windows.CloseHandle(server)
		return 0, 0, err
	}
	return server, client, nil
}

func createWindowsResultPipe() (windows.Handle, windows.Handle, error) {
	read, write, err := createWindowsOverlappedReadPipe()
	if err != nil {
		return 0, 0, err
	}
	if err := windows.SetHandleInformation(write, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		_ = windows.CloseHandle(read)
		_ = windows.CloseHandle(write)
		return 0, 0, err
	}
	return read, write, nil
}

func createWindowsInheritableNullInput() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString("NUL")
	if err != nil {
		return 0, err
	}
	security := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	return windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		security, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
}

func createWindowsTracePipe() (string, windows.Handle, error) {
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return "", 0, fmt.Errorf("random trace pipe name: %w", err)
	}
	name := `\\.\pipe\shell-picker-trace-` + hex.EncodeToString(random)
	handle, err := createWindowsTracePipeInstance(name, true)
	if err != nil {
		return "", 0, fmt.Errorf("create trace named pipe: %w", err)
	}
	return name, handle, nil
}

func createWindowsTracePipeInstance(name string, first bool) (windows.Handle, error) {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	security, err := currentUserSecurityAttributes()
	if err != nil {
		return 0, fmt.Errorf("create trace pipe security descriptor: %w", err)
	}
	flags := uint32(windows.PIPE_ACCESS_INBOUND | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(wide, flags, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES, 64<<10, 64<<10, 0, security)
}

var updateProcThreadAttribute = windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")

func updatePseudoConsoleAttribute(attributes *windows.ProcThreadAttributeListContainer, console windows.Handle) error {
	result, _, callErr := updateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(attributes.List())), 0, windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		pseudoConsoleAttributeValue(console), unsafe.Sizeof(console), 0, 0)
	if result != 0 {
		return nil
	}
	if callErr != nil {
		return callErr
	}
	return errors.New("UpdateProcThreadAttribute failed")
}

func updateWindowsHandleList(attributes *windows.ProcThreadAttributeListContainer, handles ...windows.Handle) error {
	if len(handles) == 0 {
		return errors.New("inherited handle list cannot be empty")
	}
	return attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0]))
}

// pseudoConsoleAttributeValue passes the HPCON handle value required by
// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE. Passing the address of the handle would
// change the ABI.
func pseudoConsoleAttributeValue(console windows.Handle) uintptr {
	return uintptr(console)
}

func currentUserSecurityAttributes() (*windows.SecurityAttributes, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor}, nil
}
