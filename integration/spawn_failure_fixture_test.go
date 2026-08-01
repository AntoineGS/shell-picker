package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSpawnFailureFixtureIsRegularFile(t *testing.T) {
	path := newSpawnFailureExecutable(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("spawn-failure fixture mode=%s; want regular file", info.Mode())
	}
	if runtime.GOOS == "windows" && filepath.Ext(path) != ".exe" {
		t.Fatalf("spawn-failure fixture path=%q; want .exe suffix", path)
	}
}

func newSpawnFailureExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "invalid-executable")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, []byte("not an executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
