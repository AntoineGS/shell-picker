//go:build !windows

package preview

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type converterArtifact struct {
	root, directory, held     *os.File
	directoryName, name, path string
	once                      sync.Once
}

func (source *cacheSource) Validate() error {
	now := unix.NsecToTimeval(time.Now().UnixNano())
	if err := unix.Futimes(int(source.file.Fd()), []unix.Timeval{now, now}); err != nil {
		return err
	}
	_, _, err := validateOpenFile(source.file, 1, source.identity)
	return err
}

const cacheEnvironmentVariable = "XDG_CACHE_HOME"
const cacheHomeSuffix = ".cache"

func ensureCacheRoot(path string) (fileIdentity, error) {
	root, err := openCacheRoot(path, true)
	if root == nil {
		return fileIdentity{}, err
	}
	identity, identityErr := directoryIdentity(root)
	return identity, errors.Join(err, identityErr, root.Close())
}
func openCacheRoot(path string, create bool) (*os.File, error) {
	fd, err := unix.Open(string(os.PathSeparator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), string(os.PathSeparator))
	for _, component := range strings.FieldsFunc(filepath.Clean(path), func(value rune) bool { return value == os.PathSeparator }) {
		nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if create && errors.Is(openErr, syscall.ENOENT) {
			if mkdirErr := unix.Mkdirat(int(current.Fd()), component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, syscall.EEXIST) {
				_ = current.Close()
				return nil, mkdirErr
			}
			nextFD, openErr = unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, ErrUnsafeCache
		}
		next := os.NewFile(uintptr(nextFD), component)
		_ = current.Close()
		current = next
	}
	return current, nil
}
func openCache(cache *Cache) (*os.File, error) {
	root, err := openCacheRoot(cache.root, false)
	if err != nil {
		return nil, err
	}
	identity, err := directoryIdentity(root)
	if err == nil && identity == cache.rootIdentity {
		return root, nil
	}
	_ = root.Close()
	return nil, ErrUnsafeCache
}
func directoryIdentity(directory *os.File) (fileIdentity, error) {
	info, err := directory.Stat()
	if err != nil || !info.IsDir() {
		return fileIdentity{}, ErrUnsafeCache
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, ErrUnsafeCache
	}
	return fileIdentity{uint64(stat.Dev), uint64(stat.Ino)}, nil
}
func cachePut(cache *Cache, key string, source io.Reader) error {
	root, err := openCache(cache)
	if err != nil {
		return err
	}
	defer root.Close()
	temp, tempName, err := createFileAt(root, cacheTempPrefix)
	if err != nil {
		return err
	}
	defer temp.Close()
	defer unix.Unlinkat(int(root.Fd()), tempName, 0)
	if copyErr := copyCacheData(temp, source); copyErr != nil {
		return copyErr
	}
	tempIdentity, _, err := validateOpenFile(temp, 1, fileIdentity{})
	if err != nil {
		return err
	}
	published := false
	if err = unix.Linkat(int(root.Fd()), tempName, int(root.Fd()), key, 0); err == nil {
		published = true
	} else if !errors.Is(err, syscall.EEXIST) {
		return err
	}
	if published {
		if _, _, err = validateOpenFile(temp, 2, tempIdentity); err != nil {
			return err
		}
	}
	if err = unix.Unlinkat(int(root.Fd()), tempName, 0); err != nil {
		return err
	}
	if published {
		if _, _, err = validateOpenFile(temp, 1, tempIdentity); err != nil {
			return err
		}
	}
	var winner *os.File
	for attempts := 0; attempts < 50; attempts++ {
		winner, _, _, err = openAcceptedAt(root, key, mapIdentity(published, tempIdentity))
		if !errors.Is(err, ErrUnsafeCache) || published {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if winner != nil {
		_ = winner.Close()
	}
	return err
}
func cachePrune(cache *Cache) error {
	root, err := openCache(cache)
	if err != nil {
		return err
	}
	defer root.Close()
	duplicate, err := unix.Dup(int(root.Fd()))
	if err != nil {
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
		file, identity, size, openErr := openAcceptedAt(root, entry.Name(), fileIdentity{})
		if openErr != nil {
			continue
		}
		info, statErr := file.Stat()
		_ = file.Close()
		if statErr != nil {
			continue
		}
		total = saturatedAdd(total, size)
		items = append(items, pruneItem{entry.Name(), size, info.ModTime(), identity})
	}
	pruneOldest(cache.maxBytes, total, items, func(item pruneItem) bool {
		file, _, _, openErr := openAcceptedAt(root, item.name, item.identity)
		if openErr != nil {
			return false
		}
		unlinkErr := unix.Unlinkat(int(root.Fd()), item.name, 0)
		_, links, statErr := validateOpenFile(file, 0, item.identity)
		_ = file.Close()
		return unlinkErr == nil && statErr == nil && links == 0
	})
	return nil
}
func newConverterArtifact(cache *Cache, suffix string) (*converterArtifact, error) {
	root, err := openCache(cache)
	if err != nil {
		return nil, err
	}
	directoryName, directory, err := createDirectoryAt(root)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	name := "artifact" + suffix
	file, err := openFileAt(directory, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		_ = directory.Close()
		_ = unix.Unlinkat(int(root.Fd()), directoryName, unix.AT_REMOVEDIR)
		_ = root.Close()
		return nil, err
	}
	_ = file.Close()
	path := filepath.Join(cache.root, directoryName, name)
	if runtime.GOOS == "linux" {
		path = fmt.Sprintf("/proc/%d/fd/%d/%s", os.Getpid(), directory.Fd(), name)
	}
	return &converterArtifact{root: root, directory: directory, directoryName: directoryName, name: name, path: path}, nil
}
func (artifact *converterArtifact) Size() (int64, error) {
	fd, err := unix.Openat(int(artifact.directory.Fd()), artifact.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fd), artifact.name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, ErrUnsafeCache
	}
	return info.Size(), nil
}
func (artifact *converterArtifact) OpenAccepted() (io.ReadCloser, int64, error) {
	file, _, size, err := openAcceptedAt(artifact.directory, artifact.name, fileIdentity{})
	return file, size, err
}
func (artifact *converterArtifact) Cleanup() {
	artifact.once.Do(func() {
		if artifact.held != nil {
			_ = artifact.held.Close()
		}
		_ = unix.Unlinkat(int(artifact.directory.Fd()), artifact.name, 0)
		_ = artifact.directory.Close()
		_ = unix.Unlinkat(int(artifact.root.Fd()), artifact.directoryName, unix.AT_REMOVEDIR)
		_ = artifact.root.Close()
	})
}
func openCacheSource(cache *Cache, key string) (*cacheSource, error) {
	root, err := openCache(cache)
	if err != nil {
		return nil, err
	}
	source, identity, _, err := openAcceptedAt(root, key, fileIdentity{})
	_ = root.Close()
	if err != nil {
		return nil, err
	}
	return &cacheSource{source, identity}, nil
}
func (artifact *converterArtifact) OpenWritable() (syncWriteCloser, error) {
	return openFileAt(artifact.directory, artifact.name, unix.O_WRONLY|unix.O_TRUNC, 0)
}
func (artifact *converterArtifact) Validate() error {
	file, _, _, err := openAcceptedAt(artifact.directory, artifact.name, fileIdentity{})
	if err != nil {
		return err
	}
	artifact.held = file
	if runtime.GOOS == "linux" {
		artifact.path = fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), file.Fd())
	}
	return nil
}
func createFileAt(directory *os.File, prefix string) (*os.File, string, error) {
	for attempts := 0; attempts < 100; attempts++ {
		name, err := randomCacheName(prefix)
		if err != nil {
			return nil, "", err
		}
		fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err == nil {
			return os.NewFile(uintptr(fd), name), name, nil
		}
		if !errors.Is(err, syscall.EEXIST) {
			return nil, "", err
		}
	}
	return nil, "", ErrUnsafeCache
}
func openFileAt(directory *os.File, name string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}
func openAcceptedAt(directory *os.File, name string, expected fileIdentity) (*os.File, fileIdentity, int64, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return nil, fileIdentity{}, 0, os.ErrNotExist
	}
	if err != nil {
		return nil, fileIdentity{}, 0, ErrUnsafeCache
	}
	file := os.NewFile(uintptr(fd), name)
	identity, _, sizeErr := validateOpenFile(file, 1, expected)
	if sizeErr != nil {
		_ = file.Close()
		return nil, fileIdentity{}, 0, sizeErr
	}
	info, _ := file.Stat()
	return file, identity, info.Size(), nil
}
func validateOpenFile(file *os.File, links uint64, expected fileIdentity) (fileIdentity, uint64, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCachedArtifactBytes {
		if err == nil && info.Size() > maxCachedArtifactBytes {
			return fileIdentity{}, 0, ErrArtifactLimit
		}
		return fileIdentity{}, 0, ErrUnsafeCache
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, 0, ErrUnsafeCache
	}
	identity := fileIdentity{uint64(stat.Dev), uint64(stat.Ino)}
	linkCount := uint64(stat.Nlink)
	if linkCount != links || expected != (fileIdentity{}) && identity != expected {
		return identity, linkCount, ErrUnsafeCache
	}
	return identity, linkCount, nil
}
func createDirectoryAt(root *os.File) (string, *os.File, error) {
	for attempts := 0; attempts < 100; attempts++ {
		name, err := randomCacheName(cacheTempPrefix)
		if err != nil {
			return "", nil, err
		}
		if err = unix.Mkdirat(int(root.Fd()), name, 0o700); errors.Is(err, syscall.EEXIST) {
			continue
		} else if err != nil {
			return "", nil, err
		}
		fd, err := unix.Openat(int(root.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			_ = unix.Unlinkat(int(root.Fd()), name, unix.AT_REMOVEDIR)
			return "", nil, err
		}
		return name, os.NewFile(uintptr(fd), name), nil
	}
	return "", nil, ErrUnsafeCache
}
