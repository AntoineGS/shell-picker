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
	if err := windows.CloseHandle(blocker); err != nil {
		t.Fatal(err)
	}
	if !artifact.Cleanup() {
		t.Fatal("cleanup did not succeed after renderer handle closed")
	}
	assertNoCacheTemps(t, cache.root)
}
