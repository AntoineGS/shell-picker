package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

type realBinaryBuilder func(root string) (picker, helper string, err error)

type realBinaryCache struct {
	once   sync.Once
	root   string
	picker string
	helper string
	err    error
}

var realFZFBinaryCache realBinaryCache

func (cache *realBinaryCache) paths(builder realBinaryBuilder) (picker, helper string, err error) {
	cache.once.Do(func() {
		cache.root, cache.err = os.MkdirTemp("", "shell-picker-real-fzf-binaries-")
		if cache.err != nil {
			return
		}
		cache.picker, cache.helper, cache.err = builder(cache.root)
	})
	return cache.picker, cache.helper, cache.err
}

func cachedRealBinaries(t *testing.T) (picker, helper string) {
	t.Helper()
	repository, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	picker, helper, err = realFZFBinaryCache.paths(func(root string) (string, string, error) {
		picker := filepath.Join(root, binaryName("shell-picker"))
		helper := filepath.Join(root, binaryName("task8-helper"))
		for _, build := range []struct {
			output string
			pkg    string
		}{
			{output: picker, pkg: "./cmd/shell-picker"},
			{output: helper, pkg: "./integration/testhelper"},
		} {
			arguments := append([]string{"build"}, firstFrameReproducibleBuildFlags...)
			arguments = append(arguments, "-o", build.output, build.pkg)
			command := exec.Command("go", arguments...)
			command.Dir = repository
			command.Env = append(os.Environ(), "TMPDIR="+os.Getenv("TMPDIR"))
			if output, err := command.CombinedOutput(); err != nil {
				return "", "", fmt.Errorf("build %s: %w\n%s", build.pkg, err, output)
			}
			_ = os.Chmod(build.output, 0o500)
		}
		return picker, helper, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return picker, helper
}

func binaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestRealFZFBinaryCacheBuildsOnceAndKeepsScenarioRootsDistinct(t *testing.T) {
	var cache realBinaryCache
	var callsMu sync.Mutex
	calls := 0
	paths := make(chan [2]string, 16)
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			picker, helper, err := cache.paths(func(root string) (string, string, error) {
				callsMu.Lock()
				calls++
				callsMu.Unlock()
				picker := filepath.Join(root, "picker")
				helper := filepath.Join(root, "helper")
				if err := os.WriteFile(picker, []byte("picker"), 0o500); err != nil {
					return "", "", err
				}
				if err := os.WriteFile(helper, []byte("helper"), 0o500); err != nil {
					return "", "", err
				}
				return picker, helper, nil
			})
			if err != nil {
				t.Errorf("cache paths: %v", err)
				return
			}
			paths <- [2]string{picker, helper}
		}()
	}
	wait.Wait()
	close(paths)

	callsMu.Lock()
	if calls != 1 {
		t.Fatalf("binary builder calls=%d, want 1", calls)
	}
	callsMu.Unlock()
	var first [2]string
	for path := range paths {
		if first == [2]string{} {
			first = path
			continue
		}
		if path != first {
			t.Fatalf("cache returned inconsistent paths=%v want %v", path, first)
		}
	}
	if first[0] == "" || first[1] == "" || cache.root == "" {
		t.Fatalf("cache paths/root empty: paths=%v root=%q", first, cache.root)
	}
	for range 4 {
		if left, right := t.TempDir(), t.TempDir(); left == right {
			t.Fatal("scenario roots unexpectedly shared")
		}
	}
	if filepath.Dir(first[0]) != cache.root {
		t.Fatalf("picker cache root=%q, want %q", filepath.Dir(first[0]), cache.root)
	}
}
