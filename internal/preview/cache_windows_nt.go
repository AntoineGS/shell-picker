//go:build windows

package preview

import (
	"errors"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   uintptr
	FileNameLength  uint32
	FileName        [1]uint16
}

func ntOpenAt(root windows.Handle, name string, disposition, options, access uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: root, ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	options |= windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	if options&windows.FILE_NON_DIRECTORY_FILE != 0 && access&(windows.FILE_WRITE_DATA|windows.DELETE) == 0 {
		share = windows.FILE_SHARE_READ
	}
	if options&windows.FILE_NON_DIRECTORY_FILE != 0 && access&windows.DELETE != 0 {
		share |= windows.FILE_SHARE_DELETE
	}
	err = windows.NtCreateFile(&handle, access, &attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
		share, disposition, options, 0, 0)
	return handle, err
}

func openAcceptedAt(root windows.Handle, name string, expected fileIdentity, deleting bool) (windows.Handle, fileIdentity, int64, time.Time, error) {
	access := uint32(windows.FILE_GENERIC_READ | windows.FILE_WRITE_ATTRIBUTES)
	if deleting {
		access |= windows.DELETE
	}
	handle, err := ntOpenAt(root, name, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, access)
	if errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) {
		return 0, fileIdentity{}, 0, time.Time{}, os.ErrNotExist
	}
	if err != nil {
		return 0, fileIdentity{}, 0, time.Time{}, ErrUnsafeCache
	}
	identity, _, err := validateHandle(handle, 1, expected)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return 0, fileIdentity{}, 0, time.Time{}, err
	}
	_, _, size, modified, _ := handleInformation(handle)
	return handle, identity, size, modified, nil
}

func validateHandle(handle windows.Handle, links uint32, expected fileIdentity) (fileIdentity, uint32, error) {
	identity, count, size, _, err := handleInformation(handle)
	if err != nil || count != links || size > maxCachedArtifactBytes || expected != (fileIdentity{}) && identity != expected {
		if err == nil && size > maxCachedArtifactBytes {
			return identity, count, ErrArtifactLimit
		}
		return identity, count, ErrUnsafeCache
	}
	return identity, count, nil
}

func handleInformation(handle windows.Handle) (fileIdentity, uint32, int64, time.Time, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fileIdentity{}, 0, 0, time.Time{}, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return fileIdentity{}, 0, 0, time.Time{}, ErrUnsafeCache
	}
	size := int64(uint64(info.FileSizeHigh)<<32 | uint64(info.FileSizeLow))
	identity := fileIdentity{uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}
	return identity, info.NumberOfLinks, size, time.Unix(0, info.LastWriteTime.Nanoseconds()), nil
}

func renameHandleNoReplace(handle, root windows.Handle, name string) error {
	utf16, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	nameBytes := (len(utf16) - 1) * 2
	var dummy fileRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(dummy.FileName))+nameBytes)
	info := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	info.RootDirectory, info.FileNameLength = uintptr(root), uint32(nameBytes)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&info.FileName[0]))[:nameBytes/2:nameBytes/2], utf16)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, &buffer[0], uint32(len(buffer)), windows.FileRenameInformation)
}

func deleteHandle(handle windows.Handle) error {
	flags := uint32(windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS)
	var status windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(handle, &status, (*byte)(unsafe.Pointer(&flags)),
		uint32(unsafe.Sizeof(flags)), windows.FileDispositionInformationEx)
}
