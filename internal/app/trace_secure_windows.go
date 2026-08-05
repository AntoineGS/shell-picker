//go:build windows

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"unsafe"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"golang.org/x/sys/windows"
)

const traceInitializationReadLimit = 4 << 10

type windowsTraceCreateCall struct {
	Path     string
	Access   uint32
	Share    uint32
	Creation uint32
	Flags    uint32
	Security *windows.SecurityAttributes
}

type windowsTraceOps struct {
	createFile   func(windowsTraceCreateCall) (windows.Handle, error)
	validateFile func(windows.Handle) error
	truncateFile func(windows.Handle) error
	closeHandle  func(windows.Handle) error
}

func isCanonicalNamedPipePath(path string) bool {
	const prefix = `\\.\pipe\`
	return len(path) > len(prefix) && strings.EqualFold(path[:len(prefix)], prefix)
}

func openTraceSink(path string, sessionID [16]byte) (io.WriteCloser, error) {
	return openTraceSinkWithExpectedSession(path, integrationpkg.RedactedSessionID(sessionID))
}

func openTraceSinkWithExpectedSession(path, expectedSession string) (io.WriteCloser, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	lock, err := newWindowsTraceRecordLock(path)
	if err != nil {
		return nil, err
	}
	owned := lock.created
	releaseInitialization := func() error {
		if !owned {
			return nil
		}
		owned = false
		return lock.releaseOwner()
	}
	cleanup := func(primary error) error {
		return errors.Join(primary, releaseInitialization(), lock.closeHandle())
	}
	if !owned {
		status, waitErr := lock.waitForOwner()
		if waitErr != nil {
			return nil, cleanup(waitErr)
		}
		if status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED {
			return nil, cleanup(fmt.Errorf("wait for trace record lock: unexpected wait status 0x%x", status))
		}
		owned = true
	}
	ops := windowsTraceOps{
		createFile: func(call windowsTraceCreateCall) (windows.Handle, error) {
			wide, err := windows.UTF16PtrFromString(call.Path)
			if err != nil {
				return 0, err
			}
			return windows.CreateFile(wide, call.Access, call.Share, call.Security, call.Creation, call.Flags, 0)
		},
		validateFile: validateWindowsTraceFile,
		truncateFile: func(handle windows.Handle) error {
			if _, err := windows.SetFilePointer(handle, 0, nil, windows.FILE_BEGIN); err != nil {
				return err
			}
			return windows.SetEndOfFile(handle)
		},
		closeHandle: windows.CloseHandle,
	}
	var attributes *windows.SecurityAttributes
	if !isCanonicalNamedPipePath(path) {
		security, err := currentUserTraceSecurity()
		if err != nil {
			return nil, cleanup(fmt.Errorf("create trace security descriptor: %w", err))
		}
		attributes = security.attributes
		ops.validateFile = func(handle windows.Handle) error {
			if err := validateWindowsTraceFile(handle); err != nil {
				return err
			}
			dacl, _, err := security.descriptor.DACL()
			if err != nil {
				return err
			}
			return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
		}
	}
	handle, err := openWindowsTraceHandleWithSecurityAndTruncate(path, ops, attributes, false)
	if err != nil {
		return nil, cleanup(err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, cleanup(errors.Join(errors.New("wrap trace handle"), windows.CloseHandle(handle)))
	}
	if !isCanonicalNamedPipePath(path) {
		needsTruncate, err := traceFileNeedsTruncation(file, expectedSession)
		if err != nil {
			return nil, cleanup(errors.Join(err, file.Close()))
		}
		if needsTruncate {
			if err := ops.truncateFile(handle); err != nil {
				return nil, cleanup(errors.Join(err, file.Close()))
			}
		}
	}
	if err := releaseInitialization(); err != nil {
		return nil, errors.Join(errors.New("release trace initialization lock"), file.Close(), lock.closeHandle(), err)
	}
	// The caller authorizes every ancestor and the target; elevated wrappers
	// must not accept an untrusted trace path. Final-handle DACL, type, and
	// no-follow checks are defense in depth, not anchored traversal.
	return &windowsTraceSink{file: file, lock: lock, disk: !isCanonicalNamedPipePath(path)}, nil
}

type windowsTraceSecurity struct {
	descriptor *windows.SECURITY_DESCRIPTOR
	attributes *windows.SecurityAttributes
}

func currentUserTraceSecurity() (windowsTraceSecurity, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return windowsTraceSecurity{}, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return windowsTraceSecurity{}, err
	}
	return windowsTraceSecurity{descriptor: sd, attributes: &windows.SecurityAttributes{
		Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd,
	}}, nil
}

func openWindowsTraceHandle(path string, ops windowsTraceOps) (windows.Handle, error) {
	return openWindowsTraceHandleWithSecurity(path, ops, nil)
}

func openWindowsTraceHandleWithSecurity(path string, ops windowsTraceOps, security *windows.SecurityAttributes) (windows.Handle, error) {
	return openWindowsTraceHandleWithSecurityAndTruncate(path, ops, security, true)
}

func openWindowsTraceHandleWithSecurityAndTruncate(path string, ops windowsTraceOps, security *windows.SecurityAttributes, truncate bool) (windows.Handle, error) {
	call := windowsTraceCreateCall{Path: path, Access: windows.GENERIC_WRITE, Share: windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE,
		Creation: windows.OPEN_ALWAYS, Flags: windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT,
		Security: security}
	if isCanonicalNamedPipePath(path) {
		call.Share, call.Creation, call.Flags, call.Security = 0, windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_WRITE_THROUGH, nil
	} else {
		call.Access |= windows.GENERIC_READ | windows.WRITE_DAC
	}
	handle, err := ops.createFile(call)
	if err != nil {
		return 0, err
	}
	fail := func(err error) (windows.Handle, error) { return 0, errors.Join(err, ops.closeHandle(handle)) }
	if isCanonicalNamedPipePath(path) {
		return handle, nil
	}
	if err := ops.validateFile(handle); err != nil {
		return fail(err)
	}
	if truncate {
		if err := ops.truncateFile(handle); err != nil {
			return fail(err)
		}
	}
	return handle, nil
}

func traceFileNeedsTruncation(file *os.File, expectedSession string) (bool, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	buffer := make([]byte, traceInitializationReadLimit)
	read, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	lineEnd := bytes.IndexByte(buffer[:read], '\n')
	if lineEnd < 0 {
		return true, nil
	}
	var record struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buffer[:lineEnd]), &record); err != nil {
		return true, nil
	}
	return record.Session != expectedSession, nil
}

func validateWindowsTraceFile(handle windows.Handle) error {
	kind, err := windows.GetFileType(handle)
	if err != nil {
		return err
	}
	if kind != windows.FILE_TYPE_DISK {
		return errors.New("trace sink is not a disk file")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errors.New("trace sink is reparse point or non-regular file")
	}
	return nil
}
