//go:build windows

package preview

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type converterArtifact struct {
	root, directory, held windows.Handle
	name, path            string
}

func (source *cacheSource) Validate() error {
	now := windows.NsecToFiletime(time.Now().UnixNano())
	if err := windows.SetFileTime(windows.Handle(source.file.Fd()), nil, &now, &now); err != nil {
		return err
	}
	_, _, err := validateHandle(windows.Handle(source.file.Fd()), 1, source.identity)
	return err
}

const cacheEnvironmentVariable = "LOCALAPPDATA"
const rootAccessMask = windows.FILE_GENERIC_READ
const cacheHomeSuffix = `AppData\Local`

func ensureCacheRoot(path string) (fileIdentity, error) {
	root, err := openCacheRoot(path, true)
	if err != nil {
		return fileIdentity{}, err
	}
	identity, identityErr := directoryIdentity(root)
	_ = windows.CloseHandle(root)
	return identity, identityErr
}
func openCacheRoot(path string, create bool) (windows.Handle, error) {
	volume := filepath.VolumeName(path)
	anchor := `\??\` + volume + `\`
	if strings.HasPrefix(volume, `\\`) {
		anchor = `\??\UNC\` + strings.TrimPrefix(volume, `\\`) + `\`
	}
	current, err := ntOpenAt(0, anchor, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE, rootAccessMask)
	if err != nil {
		return 0, ErrUnsafeCache
	}
	remainder := strings.Trim(filepath.Clean(strings.TrimPrefix(path, volume)), `\/`)
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool { return r == '\\' || r == '/' }) {
		disposition := uint32(windows.FILE_OPEN)
		if create {
			disposition = windows.FILE_OPEN_IF
		}
		next, openErr := ntOpenAt(current, component, disposition, windows.FILE_DIRECTORY_FILE, rootAccessMask)
		_ = windows.CloseHandle(current)
		if openErr != nil {
			_ = windows.CloseHandle(next)
			return 0, ErrUnsafeCache
		}
		current = next
	}
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(current, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(current)
		return 0, ErrUnsafeCache
	}
	return current, nil
}
func openCache(cache *Cache) (windows.Handle, error) {
	root, err := openCacheRoot(cache.root, false)
	if err != nil {
		return 0, err
	}
	identity, err := directoryIdentity(root)
	if err == nil && identity == cache.rootIdentity {
		return root, nil
	}
	_ = windows.CloseHandle(root)
	return 0, ErrUnsafeCache
}
func directoryIdentity(directory windows.Handle) (fileIdentity, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(directory, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fileIdentity{}, ErrUnsafeCache
	}
	return fileIdentity{uint64(info.VolumeSerialNumber), uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}, nil
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
	share |= (options & windows.FILE_NON_DIRECTORY_FILE) / windows.FILE_NON_DIRECTORY_FILE * (access & windows.DELETE) / windows.DELETE * windows.FILE_SHARE_DELETE
	err = windows.NtCreateFile(&handle, access, &attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, share, disposition, options, 0, 0)
	return handle, err
}
func cachePut(cache *Cache, key string, source io.Reader) error {
	root, err := openCache(cache)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(root)
	temp, _, err := createRandomAt(root, cacheTempPrefix, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = deleteHandle(temp)
		}
		_ = windows.CloseHandle(temp)
	}()
	var copyHandle windows.Handle
	process := windows.CurrentProcess()
	if err = windows.DuplicateHandle(process, temp, process, &copyHandle, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return err
	}
	writer := os.NewFile(uintptr(copyHandle), "cache-temp")
	copyErr := errors.Join(copyCacheData(writer, source), writer.Close())
	if copyErr != nil {
		return copyErr
	}
	identity, _, err := validateHandle(temp, 1, fileIdentity{})
	if err != nil {
		return err
	}
	err = renameHandleNoReplace(temp, root, key)
	if err == nil {
		published = true
	} else if !errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
		return err
	}
	if published {
		if _, _, err = validateHandle(temp, 1, identity); err != nil {
			return err
		}
	}
	winner, _, _, _, err := openAcceptedAt(root, key, mapIdentity(published, identity), true)
	_ = windows.CloseHandle(winner)
	return err
}
func cachePrune(cache *Cache) error {
	root, err := openCache(cache)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(root)
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if err = windows.DuplicateHandle(process, root, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return err
	}
	directory := os.NewFile(uintptr(duplicate), cache.root)
	entries, err := directory.Readdir(-1)
	_ = directory.Close()
	if err != nil {
		return err
	}
	items := make([]pruneItem, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if !validCacheKey(entry.Name()) {
			continue
		}
		handle, id, size, modified, openErr := openAcceptedAt(root, entry.Name(), fileIdentity{}, false)
		if openErr != nil {
			continue
		}
		_ = windows.CloseHandle(handle)
		total = saturatedAdd(total, size)
		items = append(items, pruneItem{entry.Name(), size, modified, id})
	}
	pruneOldest(cache.maxBytes, total, items, func(item pruneItem) bool {
		handle, _, _, _, openErr := openAcceptedAt(root, item.name, item.identity, true)
		removed := openErr == nil && deleteHandle(handle) == nil
		_ = windows.CloseHandle(handle)
		return removed
	})
	return nil
}
func newConverterArtifact(cache *Cache, suffix string) (*converterArtifact, error) {
	root, err := openCache(cache)
	if err != nil {
		return nil, err
	}
	directory, directoryName, err := createRandomAt(root, cacheTempPrefix, windows.FILE_DIRECTORY_FILE, rootAccessMask|windows.DELETE)
	if err != nil {
		_ = windows.CloseHandle(root)
		return nil, err
	}
	name := "artifact" + suffix
	file, err := ntOpenAt(directory, name, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE)
	if err != nil {
		_ = deleteHandle(directory)
		_ = windows.CloseHandle(directory)
		_ = windows.CloseHandle(root)
		return nil, err
	}
	_ = windows.CloseHandle(file)
	return &converterArtifact{root: root, directory: directory, name: name, path: filepath.Join(cache.root, directoryName, name)}, nil
}
func (artifact *converterArtifact) Size() (int64, error) {
	handle, err := ntOpenAt(artifact.directory, artifact.name, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_GENERIC_READ)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(handle)
	_, _, size, _, err := handleInformation(handle)
	return size, err
}
func (artifact *converterArtifact) OpenAccepted() (io.ReadCloser, int64, error) {
	handle, _, size, _, err := openAcceptedAt(artifact.directory, artifact.name, fileIdentity{}, false)
	if err != nil {
		return nil, 0, err
	}
	return os.NewFile(uintptr(handle), artifact.name), size, nil
}
func (artifact *converterArtifact) Cleanup() {
	if artifact.root == 0 {
		return
	}
	_ = windows.CloseHandle(artifact.held)
	if handle, err := ntOpenAt(artifact.directory, artifact.name, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, windows.DELETE); err == nil {
		_ = deleteHandle(handle)
		_ = windows.CloseHandle(handle)
	}
	_ = deleteHandle(artifact.directory)
	_ = windows.CloseHandle(artifact.directory)
	_ = windows.CloseHandle(artifact.root)
	artifact.root = 0
}
func openCacheSource(cache *Cache, key string) (*cacheSource, error) {
	root, err := openCache(cache)
	if err != nil {
		return nil, err
	}
	source, identity, _, _, err := openAcceptedAt(root, key, fileIdentity{}, false)
	_ = windows.CloseHandle(root)
	if err != nil {
		return nil, err
	}
	return &cacheSource{os.NewFile(uintptr(source), key), identity}, nil
}
func (artifact *converterArtifact) OpenWritable() (syncWriteCloser, error) {
	handle, err := ntOpenAt(artifact.directory, artifact.name, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), artifact.name), nil
}
func (artifact *converterArtifact) Validate() error {
	handle, _, _, _, err := openAcceptedAt(artifact.directory, artifact.name, fileIdentity{}, false)
	artifact.held = handle
	return err
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
	return windows.NtSetInformationFile(handle, &status, (*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)), windows.FileDispositionInformationEx)
}
