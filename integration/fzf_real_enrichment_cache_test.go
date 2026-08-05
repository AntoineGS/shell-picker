package integration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestRealZoxideToolCacheRebuildsAfterFixtureAndSourceRemoval(t *testing.T) {
	root := t.TempDir()
	rebuildSource := filepath.Join(root, "rebuild-source")
	writeTestExecutable(t, rebuildSource, []byte("rebuild-source-v1"))
	cache := realZoxideToolCache{root: filepath.Join(root, "cache")}
	firstTarget := filepath.Join(root, "fixture-one", binaryName("zoxide"))
	if err := os.MkdirAll(filepath.Dir(firstTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cache.copyTo(rebuildSource, firstTarget); err != nil {
		t.Fatalf("first fixture copy: %v", err)
	}
	cacheSource := filepath.Join(cache.root, binaryName("zoxide"))
	assertIndependentExecutableCopy(t, cacheSource, firstTarget)
	if err := os.Remove(firstTarget); err != nil {
		t.Fatal(err)
	}
	quarantine := cacheSource + ".quarantined"
	if err := os.Rename(cacheSource, quarantine); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(quarantine) })
	secondTarget := filepath.Join(root, "fixture-two", binaryName("zoxide"))
	if err := os.MkdirAll(filepath.Dir(secondTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cache.copyTo(rebuildSource, secondTarget); err != nil {
		t.Fatalf("reconstructed fixture copy: %v", err)
	}
	assertIndependentExecutableCopy(t, cacheSource, secondTarget)
}

func TestRealZoxideToolCacheConcurrentRebuildsAreAtomic(t *testing.T) {
	root := t.TempDir()
	rebuildSource := filepath.Join(root, "rebuild-source")
	want := bytes.Repeat([]byte("concurrent-rebuild\n"), 1024)
	writeTestExecutable(t, rebuildSource, want)
	cache := realZoxideToolCache{root: filepath.Join(root, "cache")}
	initialTarget := filepath.Join(root, "initial", binaryName("zoxide"))
	if err := os.MkdirAll(filepath.Dir(initialTarget), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cache.copyTo(rebuildSource, initialTarget); err != nil {
		t.Fatalf("initial fixture copy: %v", err)
	}
	if err := os.Remove(filepath.Join(cache.root, binaryName("zoxide"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(initialTarget); err != nil {
		t.Fatal(err)
	}

	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			target := filepath.Join(root, "fixture-"+itoa(index), binaryName("zoxide"))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				errs <- err
				return
			}
			err := cache.copyTo(rebuildSource, target)
			if err == nil {
				err = assertExecutableBytes(target, want)
			}
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertExecutableBytes(filepath.Join(cache.root, binaryName("zoxide")), want)
}

func writeTestExecutable(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertIndependentExecutableCopy(t *testing.T, source, target string) {
	t.Helper()
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(sourceInfo, targetInfo) {
		t.Fatalf("fixture executable shares cache inode: source=%q target=%q", source, target)
	}
	if runtime.GOOS != "windows" && targetInfo.Mode().Perm()&0o111 == 0 {
		t.Fatalf("fixture executable is not executable: mode=%#o", targetInfo.Mode().Perm())
	}
}

func assertExecutableBytes(path string, want []byte) error {
	got, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("executable %q contents changed", path)
	}
	return nil
}
