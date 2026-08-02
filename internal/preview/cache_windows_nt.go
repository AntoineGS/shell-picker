//go:build windows

package preview

import (
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func cacheCleanupStale(cache *Cache) {
	root, err := openCache(cache)
	if err != nil {
		return
	}
	defer windows.CloseHandle(root)
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if windows.DuplicateHandle(process, root, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS) != nil {
		return
	}
	directory := os.NewFile(uintptr(duplicate), cache.root)
	entries, readErr := directory.Readdir(-1)
	_ = directory.Close()
	if readErr != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && validPrivateStageName(entry.Name()) {
			cleanupStaleStageAt(root, entry.Name())
		}
	}
}

func validPrivateStageName(name string) bool {
	if len(name) != len(cacheTempPrefix)+32 || !strings.HasPrefix(name, cacheTempPrefix) {
		return false
	}
	_, err := hex.DecodeString(name[len(cacheTempPrefix):])
	return err == nil && name == strings.ToLower(name)
}

func cleanupStaleStageAt(root windows.Handle, name string) {
	markerContents, ok := stageMarker(name)
	if !ok {
		return
	}
	directory, directoryID, err := openStageDirectory(root, name, fileIdentity{})
	if err != nil {
		return
	}
	defer func() {
		if directory != 0 {
			_ = windows.CloseHandle(directory)
		}
	}()
	entries, err := readStageEntries(directory, name)
	markerOnly := stageMarkerOnlySet(entries)
	if err != nil || !markerOnly && !stageEntrySet(entries, false) {
		return
	}
	marker, markerID, err := openValidatedStageFile(directory, stageMarkerName, fileIdentity{})
	if err != nil {
		return
	}
	defer func() {
		if marker != 0 {
			_ = windows.CloseHandle(marker)
		}
	}()
	if err := validateStageMarker(marker, markerID, markerContents); err != nil {
		return
	}
	var artifact windows.Handle
	var artifactID fileIdentity
	if !markerOnly {
		artifact, artifactID, err = openValidatedStageFile(directory, "artifact.jpg", fileIdentity{})
		if err != nil {
			return
		}
		defer func() {
			if artifact != 0 {
				_ = windows.CloseHandle(artifact)
			}
		}()
	}
	confirmed, _, err := openStageDirectory(root, name, directoryID)
	if err != nil {
		return
	}
	confirmedEntries, readErr := readStageEntries(confirmed, name)
	_ = windows.CloseHandle(confirmed)
	if readErr != nil ||
		(!markerOnly && !stageEntrySet(confirmedEntries, false)) ||
		(markerOnly && !stageMarkerOnlySet(confirmedEntries)) ||
		validateStageMarker(marker, markerID, markerContents) != nil ||
		(!markerOnly && validateStageFile(artifact, artifactID) != nil) {
		return
	}
	if markerOnly {
		if deleteHandle(marker) != nil {
			return
		}
	} else if deleteHandle(artifact) != nil || deleteHandle(marker) != nil {
		return
	}
	if artifact != 0 {
		_ = windows.CloseHandle(artifact)
		artifact = 0
	}
	_ = windows.CloseHandle(marker)
	marker = 0
	_ = windows.CloseHandle(directory)
	directory = 0
	finalDirectory, _, err := openStageDirectory(root, name, directoryID)
	if err != nil {
		return
	}
	defer windows.CloseHandle(finalDirectory)
	remaining, readErr := readStageEntries(finalDirectory, name)
	if readErr != nil || !stageEntrySet(remaining, true) {
		return
	}
	_ = deleteHandle(finalDirectory)
}

func stageMarkerOnlySet(entries []os.FileInfo) bool {
	return len(entries) == 1 && entries[0].Name() == stageMarkerName
}

type fileRenameInformation struct {
	ReplaceIfExists uint32
	RootDirectory   uintptr
	FileNameLength  uint32
	FileName        [1]uint16
}

func (artifact *converterArtifact) Abandon() {
	artifact.mu.Lock()
	defer artifact.mu.Unlock()
	if artifact.complete {
		return
	}
	artifact.closeOwnedHandles()
	artifact.complete = true
}

func ntOpenAt(root windows.Handle, name string, disposition, options, access uint32) (windows.Handle, error) {
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	if options&windows.FILE_NON_DIRECTORY_FILE != 0 && access&(windows.FILE_WRITE_DATA|windows.DELETE) == 0 {
		share = windows.FILE_SHARE_READ
	}
	if options&windows.FILE_NON_DIRECTORY_FILE != 0 && access&windows.DELETE != 0 {
		share |= windows.FILE_SHARE_DELETE
	}
	return ntOpenAtWithSecurity(root, name, disposition, options, access, share, nil)
}

func ntOpenAtWithSecurity(root windows.Handle, name string, disposition, options, access, share uint32,
	descriptor *windows.SECURITY_DESCRIPTOR,
) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return 0, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: root, ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	options |= windows.FILE_OPEN_REPARSE_POINT | windows.FILE_SYNCHRONOUS_IO_NONALERT
	access |= windows.SYNCHRONIZE
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

func createRandomAt(root windows.Handle, prefix string, options, access uint32) (windows.Handle, string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		name, err := randomCacheName(prefix)
		if err != nil {
			return 0, "", err
		}
		handle, err := ntOpenAt(root, name, windows.FILE_CREATE, options, access)
		if err == nil {
			return handle, name, nil
		}
		if !errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			return 0, "", err
		}
	}
	return 0, "", ErrUnsafeCache
}
