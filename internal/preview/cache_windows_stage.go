//go:build windows

package preview

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stageMarkerName = ".shell-picker-owner-v1"
const stageMarkerMagic = "shell-picker-stage\x00\x01"
const privateFileAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func (artifact *converterArtifact) closeOwnedHandles() {
	if artifact.held != 0 {
		_ = windows.CloseHandle(artifact.held)
		artifact.held = 0
	}
	if artifact.directory != 0 {
		_ = windows.CloseHandle(artifact.directory)
		artifact.directory = 0
	}
	if artifact.root != 0 {
		_ = windows.CloseHandle(artifact.root)
		artifact.root = 0
	}
}

// Wine does not apply protected create-time NT descriptors. Its closest private
// representation grants only the emulated owner and LocalSystem.
var wineRuntime = windows.NewLazySystemDLL("ntdll.dll").NewProc("wine_get_version").Find() == nil
var stageMarkerWritten = func(string) {}

func stageMarker(stageName string) ([]byte, bool) {
	// ACL ownership distinguishes other principals. Windows cannot distinguish
	// a hostile process running with the same user token and able to forge both.
	if !validPrivateStageName(stageName) {
		return nil, false
	}
	nonce := stageName[len(cacheTempPrefix):]
	if _, err := hex.DecodeString(nonce); err != nil {
		return nil, false
	}
	return []byte(stageMarkerMagic + nonce), true
}

func privateSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	owner := user.User.Sid
	descriptor, err := windows.SecurityDescriptorFromString("O:" + owner.String() + "D:P(A;;FA;;;" + owner.String() + ")")
	return descriptor, owner, err
}

func createPrivateAt(root windows.Handle, name string, options, access uint32) (windows.Handle, error) {
	descriptor, owner, err := privateSecurityDescriptor()
	if err != nil {
		return 0, err
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := ntOpenAtWithSecurity(root, name, windows.FILE_CREATE, options,
		access|windows.WRITE_DAC|windows.WRITE_OWNER, share, descriptor)
	if err != nil {
		return 0, err
	}
	if !wineRuntime {
		dacl, _, daclErr := descriptor.DACL()
		if daclErr != nil {
			err = daclErr
		} else {
			err = windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, dacl, nil)
		}
	}
	if err == nil {
		err = validatePrivateHandle(handle)
	}
	if err != nil {
		_ = deleteHandle(handle)
		_ = windows.CloseHandle(handle)
		return 0, err
	}
	return handle, nil
}

func validatePrivateHandle(handle windows.Handle) error {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return ErrUnsafeCache
	}
	owner, defaulted, err := descriptor.Owner()
	if err != nil || defaulted {
		return ErrUnsafeCache
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !owner.Equals(user.User.Sid) {
		return ErrUnsafeCache
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || !wineRuntime && control&windows.SE_DACL_PROTECTED == 0 {
		return ErrUnsafeCache
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil || defaulted || dacl == nil {
		return ErrUnsafeCache
	}
	if wineRuntime {
		return validateWinePrivateACL(dacl, user.User.Sid)
	}
	if dacl.AceCount != 1 {
		return ErrUnsafeCache
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Header.AceFlags != 0 || ace.Mask != privateFileAccess {
		return ErrUnsafeCache
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.Equals(user.User.Sid) {
		return ErrUnsafeCache
	}
	return nil
}

func validateWinePrivateACL(dacl *windows.ACL, owner *windows.SID) error {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil || dacl.AceCount != 2 {
		return ErrUnsafeCache
	}
	foundOwner, foundSystem := false, false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, index, &ace) != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Mask != privateFileAccess {
			return ErrUnsafeCache
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(owner):
			foundOwner = true
		case sid.Equals(system):
			foundSystem = true
		default:
			return ErrUnsafeCache
		}
	}
	if !foundOwner || !foundSystem {
		return ErrUnsafeCache
	}
	return nil
}

func createStageMarker(directory windows.Handle, stageName string) (fileIdentity, error) {
	contents, ok := stageMarker(stageName)
	if !ok {
		return fileIdentity{}, ErrUnsafeCache
	}
	handle, err := createPrivateAt(directory, stageMarkerName, windows.FILE_NON_DIRECTORY_FILE,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE)
	if err != nil {
		return fileIdentity{}, err
	}
	identity, _, err := validateHandle(handle, 1, fileIdentity{})
	if err == nil {
		err = validatePrivateHandle(handle)
	}
	if err != nil {
		_ = deleteHandle(handle)
		_ = windows.CloseHandle(handle)
		return fileIdentity{}, err
	}
	var writeHandle windows.Handle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, handle, process, &writeHandle, 0, false,
		windows.DUPLICATE_SAME_ACCESS); err != nil {
		_ = deleteHandle(handle)
		_ = windows.CloseHandle(handle)
		return fileIdentity{}, err
	}
	file := os.NewFile(uintptr(writeHandle), stageMarkerName)
	written, writeErr := file.Write(contents)
	if writeErr == nil && written != len(contents) {
		writeErr = io.ErrShortWrite
	}
	writeErr = errors.Join(writeErr, file.Sync(), file.Close())
	if writeErr != nil {
		_ = deleteHandle(handle)
		_ = windows.CloseHandle(handle)
		return fileIdentity{}, writeErr
	}
	stageMarkerWritten(stageName)
	err = validateStageFile(handle, identity)
	if err == nil {
		err = validateStageMarker(handle, identity, contents)
	}
	if err != nil {
		_ = deleteHandle(handle)
	}
	_ = windows.CloseHandle(handle)
	return identity, err
}

func cleanupStageMarker(directory windows.Handle, expected fileIdentity) bool {
	handle, _, err := openValidatedStageFile(directory, stageMarkerName, expected)
	if err != nil {
		return false
	}
	deleted := deleteHandle(handle) == nil
	_ = windows.CloseHandle(handle)
	return deleted
}

func openValidatedStageFile(directory windows.Handle, name string, expected fileIdentity) (windows.Handle, fileIdentity, error) {
	access := uint32(windows.FILE_GENERIC_READ | windows.DELETE)
	handle, err := ntOpenAtWithSecurity(directory, name, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE,
		access, windows.FILE_SHARE_READ, nil)
	if err != nil {
		return 0, fileIdentity{}, ErrUnsafeCache
	}
	identity, _, err := validateHandle(handle, 1, expected)
	if err == nil {
		err = validatePrivateHandle(handle)
	}
	if err != nil {
		_ = windows.CloseHandle(handle)
		return 0, fileIdentity{}, err
	}
	return handle, identity, nil
}

func validateStageMarker(handle windows.Handle, expected fileIdentity, contents []byte) error {
	identity, _, size, _, err := handleInformation(handle)
	if err != nil || identity != expected || size != int64(len(contents)) {
		return ErrUnsafeCache
	}
	if _, err := windows.SetFilePointer(handle, 0, nil, windows.FILE_BEGIN); err != nil {
		return ErrUnsafeCache
	}
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if windows.DuplicateHandle(process, handle, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS) != nil {
		return ErrUnsafeCache
	}
	reader := os.NewFile(uintptr(duplicate), stageMarkerName)
	data, readErr := io.ReadAll(io.LimitReader(reader, int64(len(contents)+1)))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(data, contents) {
		return ErrUnsafeCache
	}
	identityAfter, _, _, _, err := handleInformation(handle)
	if err != nil || identityAfter != expected {
		return ErrUnsafeCache
	}
	return validatePrivateHandle(handle)
}

func validateStageFile(handle windows.Handle, expected fileIdentity) error {
	if _, _, err := validateHandle(handle, 1, expected); err != nil {
		return err
	}
	return validatePrivateHandle(handle)
}

func createRandomPrivateAt(root windows.Handle, prefix string, options, access uint32) (windows.Handle, string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		name, err := randomCacheName(prefix)
		if err != nil {
			return 0, "", err
		}
		handle, err := createPrivateAt(root, name, options, access)
		if err == nil {
			return handle, name, nil
		}
		if !errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
			return 0, "", err
		}
	}
	return 0, "", ErrUnsafeCache
}

func stageEntrySet(entries []os.FileInfo, empty bool) bool {
	if empty {
		return len(entries) == 0
	}
	if len(entries) != 2 {
		return false
	}
	foundArtifact, foundMarker := false, false
	for _, entry := range entries {
		switch entry.Name() {
		case "artifact.jpg":
			foundArtifact = true
		case stageMarkerName:
			foundMarker = true
		default:
			return false
		}
	}
	return foundArtifact && foundMarker
}

func readStageEntries(directory windows.Handle, name string) ([]os.FileInfo, error) {
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if windows.DuplicateHandle(process, directory, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS) != nil {
		return nil, ErrUnsafeCache
	}
	stream := os.NewFile(uintptr(duplicate), name)
	entries, err := stream.Readdir(-1)
	_ = stream.Close()
	return entries, err
}

func stageDirectoryIdentity(handle windows.Handle) (fileIdentity, error) {
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks != 1 {
		return fileIdentity{}, ErrUnsafeCache
	}
	if err := validatePrivateHandle(handle); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}, nil
}

func openStageDirectory(root windows.Handle, name string, expected fileIdentity) (windows.Handle, fileIdentity, error) {
	access := uint32(rootAccessMask | windows.DELETE)
	handle, err := ntOpenAtWithSecurity(root, name, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE,
		access, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil)
	if err != nil {
		return 0, fileIdentity{}, ErrUnsafeCache
	}
	identity, err := stageDirectoryIdentity(handle)
	if err != nil || expected != (fileIdentity{}) && identity != expected {
		_ = windows.CloseHandle(handle)
		return 0, fileIdentity{}, ErrUnsafeCache
	}
	return handle, identity, nil
}
