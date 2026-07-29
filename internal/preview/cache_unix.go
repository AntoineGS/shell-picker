//go:build !windows

package preview

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func safeCacheObject(_ string, info os.FileInfo, directory bool) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if directory {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}

func publishNoReplace(temp, target string) (bool, error) {
	err := os.Link(temp, target)
	if err == nil {
		if removeErr := os.Remove(temp); removeErr != nil {
			return true, removeErr
		}
		return true, nil
	}
	if errors.Is(err, os.ErrExist) {
		return false, os.Remove(temp)
	}
	return false, err
}

func refreshCacheTime(path string) error {
	now := time.Now()
	times := []unix.Timespec{unix.NsecToTimespec(now.UnixNano()), unix.NsecToTimespec(now.UnixNano())}
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, times, unix.AT_SYMLINK_NOFOLLOW)
}

func openSafeCacheArtifact(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrUnsafeCache
	}
	file := os.NewFile(uintptr(fd), path)
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
	err = os.Remove(path)
	if errors.Is(err, syscall.EISDIR) {
		return ErrUnsafeCache
	}
	return err
}
