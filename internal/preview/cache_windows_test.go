//go:build windows

package preview

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsCachePutRootSwapIsRejectedOrExplicitlyDenied(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "cache")
	cache := mustNewCache(t, root, 512<<20)
	reader := newBarrierReader("stable")
	key := strings.Repeat("8", 64)
	done := make(chan error, 1)
	go func() { _, err := cache.Put(key, reader); done <- err }()
	<-reader.started
	oldRoot := root + "-old"
	swapErr := os.Rename(root, oldRoot)
	if swapErr == nil {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	close(reader.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if swapErr == nil {
		if _, ok := cache.Get(key); ok {
			t.Fatal("Get accepted replacement root")
		}
		data, err := os.ReadFile(filepath.Join(oldRoot, key))
		if err != nil || string(data) != "stable" {
			t.Fatalf("anchored winner=%q err=%v", data, err)
		}
		return
	}
	handle, err := openCache(cache)
	if err != nil {
		t.Fatalf("denied swap changed root identity: swap=%v open=%v", swapErr, err)
	}
	_ = windows.CloseHandle(handle)
	data, err := os.ReadFile(filepath.Join(root, key))
	if err != nil || string(data) != "stable" {
		t.Fatalf("denied swap winner=%q err=%v swap=%v", data, err, swapErr)
	}
}

func TestWindowsCacheRejectsSymlinkRootAndEntry(t *testing.T) {
	realRoot := t.TempDir()
	symlinkRoot := filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCache(symlinkRoot, 512<<20); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("root error=%v", err)
	}
	cache := mustNewCache(t, realRoot, 512<<20)
	key := strings.Repeat("a", 64)
	if err := os.Symlink(filepath.Join(realRoot, "target"), filepath.Join(realRoot, key)); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Get(key); ok {
		t.Fatal("accepted symlink cache entry")
	}
}

func TestWindowsArtifactCleanupRetriesAfterSharingViolation(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	artifact, err := newConverterArtifact(cache, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Cleanup()
	writer, err := artifact.OpenWritable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("stable")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(artifact.Path())
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := windows.CreateFile(pointer, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Cleanup() {
		t.Fatal("cleanup completed while renderer handle denied delete sharing")
	}
	rootHandle, directoryHandle := artifact.root, artifact.directory
	artifact.Abandon()
	if err := windows.CloseHandle(rootHandle); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("root handle remained open: %v", err)
	}
	if err := windows.CloseHandle(directoryHandle); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("directory handle remained open: %v", err)
	}
	if err := windows.CloseHandle(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCache(cache.root, 512<<20); err != nil {
		t.Fatal(err)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestWindowsStaleCleanupLeavesUnvalidatedPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	name := cacheTempPrefix + strings.Repeat("a", 32)
	directory := filepath.Join(root, name)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	bait := filepath.Join(directory, "artifact-attacker.jpg")
	if err := os.WriteFile(bait, []byte("attacker"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCache(root, 512<<20); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bait)
	if err != nil || string(data) != "attacker" {
		t.Fatalf("unvalidated directory changed: data=%q err=%v", data, err)
	}
}

func TestWindowsStageCreationValidationFailureLeavesNoPrivateArtifact(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	oldHook := cacheArtifactCreated
	cacheArtifactCreated = func() {
		path := stagedArtifactPath(t, cache.root)
		if err := os.Link(path, filepath.Join(t.TempDir(), "attacker-link")); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { cacheArtifactCreated = oldHook })
	artifact, err := newConverterArtifact(cache, ".jpg")
	if artifact != nil || !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
	assertNoCacheTemps(t, cache.root)
}
