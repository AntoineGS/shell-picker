//go:build windows

package preview

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

type converterArtifact struct {
	root, directory, held windows.Handle
	name, path            string
	identity, marker      fileIdentity
	mu                    sync.Mutex
	complete              bool
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

var cacheArtifactCreated = func(string) {}

func ensureCacheRoot(path string) (fileIdentity, error) {
	root, err := openCacheRoot(path, true)
	if err != nil {
		return fileIdentity{}, err
	}
	identity, identityErr := directoryIdentity(root)
	_ = windows.CloseHandle(root)
	if identityErr != nil {
		return fileIdentity{}, identityErr
	}
	reopened, err := openCacheRoot(path, false)
	if err != nil {
		return fileIdentity{}, err
	}
	reopenedIdentity, identityErr := directoryIdentity(reopened)
	_ = windows.CloseHandle(reopened)
	if identityErr != nil || reopenedIdentity != identity {
		return fileIdentity{}, ErrUnsafeCache
	}
	return identity, nil
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
	cacheCleanupStale(cache)
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
		return quarantinePrune(cache, item)
	})
	return nil
}
func quarantinePrune(cache *Cache, item pruneItem) bool {
	root, err := openCache(cache)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(root)
	handle, err := ntOpenAt(root, item.name, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_GENERIC_READ|windows.DELETE)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	quarantine, err := randomCacheName(cacheTempPrefix + "prune-")
	if err != nil || renameHandleNoReplace(handle, root, quarantine) != nil {
		return false
	}
	accepted, _, _, _, openErr := openAcceptedAt(root, quarantine, item.identity, true)
	if openErr != nil {
		_ = renameHandleNoReplace(handle, root, item.name)
		return false
	}
	removed := deleteHandle(accepted) == nil
	_ = windows.CloseHandle(accepted)
	return removed
}
func newConverterArtifact(cache *Cache, suffix string) (*converterArtifact, error) {
	root, err := openCache(cache)
	if err != nil {
		return nil, err
	}
	directory, directoryName, err := createRandomPrivateAt(root, cacheTempPrefix, windows.FILE_DIRECTORY_FILE,
		rootAccessMask|windows.DELETE)
	if err != nil {
		_ = windows.CloseHandle(root)
		return nil, err
	}
	if _, err = stageDirectoryIdentity(directory); err != nil {
		_ = deleteHandle(directory)
		_ = windows.CloseHandle(directory)
		_ = windows.CloseHandle(root)
		return nil, err
	}
	marker, err := createStageMarker(directory, directoryName)
	if err != nil {
		_ = deleteHandle(directory)
		_ = windows.CloseHandle(directory)
		_ = windows.CloseHandle(root)
		return nil, err
	}
	name := "artifact" + suffix
	file, err := createPrivateAt(directory, name, windows.FILE_NON_DIRECTORY_FILE,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE)
	if err != nil {
		cleanupStageMarker(directory, marker)
		_ = deleteHandle(directory)
		_ = windows.CloseHandle(directory)
		_ = windows.CloseHandle(root)
		return nil, err
	}
	artifact := &converterArtifact{root: root, directory: directory, name: name,
		path: filepath.Join(cache.root, directoryName, name), marker: marker}
	cacheArtifactCreated(artifact.path)
	identity, _, err := validateHandle(file, 1, fileIdentity{})
	if err == nil {
		err = validatePrivateHandle(file)
	}
	if err != nil {
		_ = windows.CloseHandle(file)
		_ = artifact.Cleanup()
		return nil, err
	}
	_ = windows.CloseHandle(file)
	artifact.identity = identity
	return artifact, nil
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
	handle, _, size, _, err := openAcceptedAt(artifact.directory, artifact.name, artifact.identity, false)
	if err != nil {
		return nil, 0, err
	}
	return os.NewFile(uintptr(handle), artifact.name), size, nil
}
func (artifact *converterArtifact) Cleanup() bool {
	artifact.mu.Lock()
	defer artifact.mu.Unlock()
	if artifact.complete {
		return true
	}
	if artifact.held != 0 {
		_ = windows.CloseHandle(artifact.held)
		artifact.held = 0
	}
	handle, err := ntOpenAt(artifact.directory, artifact.name, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, windows.DELETE)
	if err == nil {
		err = deleteHandle(handle)
		_ = windows.CloseHandle(handle)
	}
	if err != nil && !errors.Is(err, windows.STATUS_OBJECT_NAME_NOT_FOUND) {
		return false
	}
	if !cleanupStageMarker(artifact.directory, artifact.marker) {
		return false
	}
	if err = deleteHandle(artifact.directory); err != nil {
		return false
	}
	_ = windows.CloseHandle(artifact.directory)
	_ = windows.CloseHandle(artifact.root)
	artifact.complete = true
	return true
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
	if _, _, err = validateHandle(handle, 1, artifact.identity); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	file := os.NewFile(uintptr(handle), artifact.name)
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
func (artifact *converterArtifact) RenderFiles() []*os.File { return nil }
func (artifact *converterArtifact) Validate() error {
	handle, _, _, _, err := openAcceptedAt(artifact.directory, artifact.name, artifact.identity, false)
	artifact.held = handle
	return err
}
