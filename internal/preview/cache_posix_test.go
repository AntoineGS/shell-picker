//go:build linux || darwin

package preview

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPOSIXCacheRejectsSymlinkRootAndEntry(t *testing.T) {
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
