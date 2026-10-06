package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFirstFrameRepositoryRootPreservesWhitespace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support trailing spaces in directory names")
	}
	root := filepath.Join(t.TempDir(), "repository with trailing space ")
	init := exec.Command("git", "init", "-q", root)
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("initialize repository: %v\n%s", err, output)
	}
	t.Chdir(root)
	got, err := firstFrameRepositoryRoot()
	if err != nil || got != root {
		t.Fatalf("repository root=%q err=%v; want %q", got, err, root)
	}
}

func TestFirstFrameRepositoryRootIgnoresNestedTemporaryGitDirectory(t *testing.T) {
	root := t.TempDir()
	init := exec.Command("git", "init", "-q", root)
	if output, err := init.CombinedOutput(); err != nil {
		t.Fatalf("initialize repository: %v\n%s", err, output)
	}
	nested := filepath.Join(root, "integration")
	if err := os.MkdirAll(filepath.Join(nested, ".git", "test-tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	got, err := firstFrameRepositoryRoot()
	if err != nil || got != root {
		t.Fatalf("repository root=%q err=%v; want %q", got, err, root)
	}
}
