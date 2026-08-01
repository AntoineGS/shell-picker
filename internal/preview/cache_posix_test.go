//go:build linux || darwin

package preview

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSequentialRenderersReceiveIndependentStageOffsets(t *testing.T) {
	tools := t.TempDir()
	source := filepath.Join(t.TempDir(), "renderer.go")
	program := `package main
import("io";"os";"path/filepath")
func main(){ f:=os.NewFile(3,"stage"); b,_:=io.ReadAll(f); if filepath.Base(os.Args[0])=="chafa" { os.Stdout.Write(b) } }`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	kitten := filepath.Join(tools, "kitten")
	if output, err := exec.Command("go", "build", "-o", kitten, source).CombinedOutput(); err != nil {
		t.Fatalf("build renderer: %v: %s", err, output)
	}
	data, err := os.ReadFile(kitten)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "chafa"), data, 0o700); err != nil {
		t.Fatal(err)
	}
	document := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(document, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	candidate := resolved(document)
	mustPut(t, cache, cache.Key(candidate, "pdf-pdftoppm-v1"), "stable")
	var output bytes.Buffer
	options := testOptions(&output)
	options.Cache = cache
	options.Environment = []string{"PATH=" + tools, "TERM=xterm-kitty"}
	if err := Render(context.Background(), candidate, options); err != nil {
		t.Fatal(err)
	}
	if output.String() != "stable" {
		t.Fatalf("output=%q", output.String())
	}
}

func TestPOSIXStageCreationValidationFailureLeavesNoPrivateArtifact(t *testing.T) {
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

func TestPOSIXStageReplacementAcceptanceFailureCleansPrivateArtifact(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	artifact, err := newConverterArtifact(cache, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	path := stagedArtifactPath(t, cache.root)
	replaceWithDistinctInode(t, path, []byte("replacement"))
	reader, _, err := artifact.OpenAccepted()
	if reader != nil {
		_ = reader.Close()
	}
	if !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("accept error=%v", err)
	}
	if !artifact.Cleanup() {
		t.Fatal("anchored cleanup failed")
	}
	assertNoCacheTemps(t, cache.root)
}

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
