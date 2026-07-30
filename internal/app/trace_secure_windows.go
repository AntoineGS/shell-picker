//go:build windows

package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

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

func openTraceSink(path string) (io.WriteCloser, error) {
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
			return nil, fmt.Errorf("create trace security descriptor: %w", err)
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
	handle, err := openWindowsTraceHandleWithSecurity(path, ops, attributes)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, errors.Join(errors.New("wrap trace handle"), windows.CloseHandle(handle))
	}
	// The caller authorizes the sink path. Windows permits setting the opened
	// file DACL by handle, but does not provide a single API that also proves
	// every traversed parent's ACL/reparse identity; parent trust remains
	// caller-owned while the final opened object is validated without following.
	return file, nil
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
	call := windowsTraceCreateCall{Path: path, Access: windows.GENERIC_WRITE, Share: windows.FILE_SHARE_READ,
		Creation: windows.OPEN_ALWAYS, Flags: windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT,
		Security: security}
	if isCanonicalNamedPipePath(path) {
		call.Share, call.Creation, call.Flags, call.Security = 0, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, nil
	} else {
		call.Access |= windows.WRITE_DAC
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
	if err := ops.truncateFile(handle); err != nil {
		return fail(err)
	}
	return handle, nil
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
