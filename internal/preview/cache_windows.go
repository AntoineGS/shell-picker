//go:build windows

package preview

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32    = syscall.NewLazyDLL("kernel32.dll")
	moveFileExProc = modKernel32.NewProc("MoveFileExW")
)

func safeCacheObject(path string, info os.FileInfo, directory bool) bool {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := syscall.GetFileAttributes(pointer)
	if err != nil || attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	if directory {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}

func publishNoReplace(temp, target string) (bool, error) {
	from, err := syscall.UTF16PtrFromString(temp)
	if err != nil {
		return false, err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return false, err
	}
	result, _, callErr := moveFileExProc.Call(uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)), 0)
	if result != 0 {
		return true, nil
	}
	if errors.Is(callErr, syscall.ERROR_FILE_EXISTS) || errors.Is(callErr, syscall.ERROR_ALREADY_EXISTS) {
		return false, os.Remove(temp)
	}
	return false, callErr
}

func refreshCacheTime(path string) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	now := windows.NsecToFiletime(time.Now().UnixNano())
	return windows.SetFileTime(handle, nil, &now, &now)
}

func openSafeCacheArtifact(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, ErrUnsafeCache
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &data); err != nil || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, ErrUnsafeCache
	}
	file := os.NewFile(uintptr(handle), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCachedArtifactBytes {
		_ = file.Close()
		return nil, ErrUnsafeCache
	}
	return file, nil
}

func stageCacheArtifact(cache *Cache, path string) (string, func(), error) {
	if err := inspectCacheRoot(cache.root, false); err != nil {
		return "", nil, err
	}
	source, err := openSafeCacheArtifact(path)
	if err != nil {
		return "", nil, err
	}
	defer source.Close()
	directory, err := os.MkdirTemp(cache.root, cacheTempPrefix)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	targetPath := filepath.Join(directory, "artifact.jpg")
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = io.Copy(target, source)
	}
	if err == nil {
		err = target.Sync()
	}
	if target != nil {
		err = errors.Join(err, target.Close())
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return targetPath, cleanup, nil
}

func removeSafeCacheArtifact(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !safeCacheObject(path, info, false) {
		return ErrUnsafeCache
	}
	return os.Remove(path)
}
