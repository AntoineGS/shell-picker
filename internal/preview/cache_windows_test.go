//go:build windows

package preview

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func init() { readStageDirectory = readStageDirectoryWindows }

func readStageDirectoryWindows(path string) ([]os.DirEntry, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	entries, readErr := file.ReadDir(-1)
	return entries, errors.Join(readErr, file.Close())
}

func TestWindowsCachePutRootSwapIsRejectedOrExplicitlyDenied(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "cache")
	cache := mustNewCache(t, root, 512<<20)
	reader := newBarrierReader(t, "stable")
	key := strings.Repeat("8", 64)
	done := make(chan error, 1)
	go func() { _, err := cache.Put(key, reader); done <- err }()
	awaitPreview(t, reader.started, "Windows cache Put reader start across root swap")
	oldRoot := root + "-old"
	swapErr := os.Rename(root, oldRoot)
	if swapErr == nil {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	reader.release()
	if err := awaitPreview(t, done, "Windows cache Put completion across root swap"); err != nil {
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
	t.Cleanup(func() { cleanupConverterArtifact(t, artifact) })
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
	attackerRoot := t.TempDir()
	attackerLink := filepath.Join(attackerRoot, "attacker-link")
	oldHook := cacheArtifactCreated
	cacheArtifactCreated = func(path string) {
		if err := os.Link(path, attackerLink); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { cacheArtifactCreated = oldHook })
	artifact, err := newConverterArtifact(cache, ".jpg")
	if artifact != nil || !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
	if _, err := os.ReadFile(attackerLink); err != nil {
		t.Fatalf("attacker link unreadable: %v", err)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestWindowsArtifactValidationFailureClosesOwnedDirectoryHandles(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	attacker := filepath.Join(t.TempDir(), "attacker-link")
	oldHook := cacheArtifactCreated
	cacheArtifactCreated = func(path string) {
		if err := os.Link(path, attacker); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { cacheArtifactCreated = oldHook })
	artifact, err := newConverterArtifact(cache, ".jpg")
	if artifact != nil || !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
	assertNoCacheTemps(t, cache.root)
	if err := os.RemoveAll(cache.root); err != nil {
		t.Fatalf("cache root remains held: %v", err)
	}
}

func TestWindowsRejectedArtifactCleanupReleasesHandlesAfterSharingViolation(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	attacker := filepath.Join(t.TempDir(), "attacker-link")
	var blocker windows.Handle
	oldHook := cacheArtifactCreated
	cacheArtifactCreated = func(path string) {
		marker := filepath.Join(filepath.Dir(path), stageMarkerName)
		pointer, err := windows.UTF16PtrFromString(marker)
		if err != nil {
			t.Fatal(err)
		}
		blocker, err = windows.CreateFile(pointer, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Link(path, attacker); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cacheArtifactCreated = oldHook
		if blocker != 0 {
			_ = windows.CloseHandle(blocker)
		}
	})
	artifact, err := newConverterArtifact(cache, ".jpg")
	if artifact != nil || !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
	if blocker == 0 {
		t.Fatal("sharing-violation blocker was not installed")
	}
	if err := windows.CloseHandle(blocker); err != nil {
		t.Fatal(err)
	}
	blocker = 0
	attackerBefore, err := os.ReadFile(attacker)
	if err != nil {
		t.Fatalf("attacker link unreadable before stale cleanup: %v", err)
	}
	if _, err := NewCache(cache.root, 512<<20); err != nil {
		t.Fatalf("startup stale cleanup: %v", err)
	}
	assertNoCacheTemps(t, cache.root)
	attackerAfter, err := os.ReadFile(attacker)
	if err != nil || !bytes.Equal(attackerAfter, attackerBefore) {
		t.Fatalf("attacker link changed: before=%q after=%q err=%v", attackerBefore, attackerAfter, err)
	}
}
