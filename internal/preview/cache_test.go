package preview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	processpkg "github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestCacheKeyUsesExactIdentityFields(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	base := protocol.ResolvedCandidate{Path: []byte{'/', 'x', 0xff}, Size: 10, ModTimeUnixNano: 20}
	keys := []string{
		cache.Key(base, "pdf-v1"),
		cache.Key(protocol.ResolvedCandidate{Path: base.Path, Size: 11, ModTimeUnixNano: 20}, "pdf-v1"),
		cache.Key(protocol.ResolvedCandidate{Path: base.Path, Size: 10, ModTimeUnixNano: 21}, "pdf-v1"),
		cache.Key(base, "pdf-v2"),
	}
	seen := make(map[string]bool)
	for _, key := range keys {
		if len(key) != 64 || seen[key] {
			t.Fatalf("keys=%v", keys)
		}
		seen[key] = true
	}
	var raw bytes.Buffer
	raw.WriteByte(1)
	_ = binary.Write(&raw, binary.BigEndian, uint64(len(base.Path)))
	raw.Write(base.Path)
	_ = binary.Write(&raw, binary.BigEndian, base.Size)
	_ = binary.Write(&raw, binary.BigEndian, base.ModTimeUnixNano)
	_ = binary.Write(&raw, binary.BigEndian, uint64(len("pdf-v1")))
	raw.WriteString("pdf-v1")
	want := sha256.Sum256(raw.Bytes())
	if keys[0] != hex.EncodeToString(want[:]) {
		t.Fatalf("key=%q want=%x", keys[0], want)
	}
}

func TestCachePutIsAtomicAndPrunesOldest(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 12)
	old := strings.Repeat("a", 64)
	newer := strings.Repeat("b", 64)
	mustPut(t, cache, old, "12345678")
	oldTime := time.Unix(10, 0)
	if err := os.Chtimes(filepath.Join(cache.root, old), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	mustPut(t, cache, newer, "abcdefgh")
	if _, ok := cache.Get(old); ok {
		t.Fatal("old cache entry survived limit")
	}
	path, ok := cache.Get(newer)
	if !ok {
		t.Fatal("new cache entry missing")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "abcdefgh" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestCacheRejectsUnsafeRootEntryAndOversizedArtifact(t *testing.T) {
	if runtime.GOOS != "windows" {
		realRoot := t.TempDir()
		symlinkRoot := filepath.Join(t.TempDir(), "cache")
		if err := os.Symlink(realRoot, symlinkRoot); err != nil {
			t.Fatal(err)
		}
		if _, err := NewCache(symlinkRoot, 512<<20); !errors.Is(err, ErrUnsafeCache) {
			t.Fatalf("root err=%v", err)
		}
	}
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	key := strings.Repeat("a", 64)
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(cache.root, "target"), filepath.Join(cache.root, key)); err != nil {
			t.Fatal(err)
		}
		if _, ok := cache.Get(key); ok {
			t.Fatal("accepted symlink cache entry")
		}
	}
	tooLarge := io.LimitReader(zeroReader{}, maxCachedArtifactBytes+1)
	if _, err := cache.Put("b"+strings.Repeat("0", 63), tooLarge); !errors.Is(err, ErrArtifactLimit) {
		t.Fatalf("artifact err=%v", err)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestCacheTwoWritersPublishSameKeyWithoutOverwrite(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	key := strings.Repeat("c", 64)
	temps := []string{makeCacheTemp(t, cache, "first"), makeCacheTemp(t, cache, "second")}
	start := make(chan struct{})
	results := make(chan bool, 2)
	errorsSeen := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, temp := range temps {
		go func(path string) {
			ready.Done()
			<-start
			published, err := publishNoReplace(path, filepath.Join(cache.root, key))
			results <- published
			errorsSeen <- err
		}(temp)
	}
	ready.Wait()
	close(start)
	publishers := 0
	for range temps {
		if <-results {
			publishers++
		}
		if err := <-errorsSeen; err != nil {
			t.Fatal(err)
		}
	}
	if publishers != 1 {
		t.Fatalf("publishers=%d", publishers)
	}
	data, err := os.ReadFile(filepath.Join(cache.root, key))
	if err != nil || string(data) != "first" && string(data) != "second" {
		t.Fatalf("winner=%q err=%v", data, err)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestCacheLoserRejectsSymlinkPublicationAttack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	key := strings.Repeat("d", 64)
	target := filepath.Join(cache.root, key)
	if err := os.Symlink(filepath.Join(cache.root, "attacker"), target); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Put(key, strings.NewReader("safe")); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("attack err=%v", err)
	}
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("target changed: mode=%v err=%v", info.Mode(), err)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestCachedArtifactStagingDoesNotFollowSwappedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	key := strings.Repeat("e", 64)
	path := mustPut(t, cache, key, "safe")
	attacker := filepath.Join(t.TempDir(), "attacker")
	if err := os.WriteFile(attacker, []byte("unsafe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stageCacheArtifact(cache, path); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("stage err=%v", err)
	}
}

func TestCacheOperationsRejectRootSwappedToSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	parent, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(parent, "cache")
	cache := mustNewCache(t, root, 512<<20)
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("f", 64)
	if _, err := cache.Put(key, strings.NewReader("unsafe")); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("put err=%v", err)
	}
	if _, ok := cache.Get(key); ok {
		t.Fatal("Get followed swapped cache root")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside entries=%v err=%v", entries, err)
	}
}

func TestPDFConverterPublishesCacheAndCacheHitSkipsConverter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct shell process fixture is Unix-specific")
	}
	tools, converterLog, rendererLog := t.TempDir(), filepath.Join(t.TempDir(), "converter"), filepath.Join(t.TempDir(), "renderer")
	converter := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nprintf jpeg > \"$4.jpg\"\n", strconv.Quote(converterLog))
	renderer := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nprintf rendered\n", strconv.Quote(rendererLog))
	if err := os.WriteFile(filepath.Join(tools, "pdftoppm"), []byte(converter), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tools, "chafa"), []byte(renderer), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	candidate := resolved(path)
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		options := testOptions(&output)
		options.Cache, options.Environment = cache, []string{"PATH=" + tools}
		starts, live, maximum := 0, 0, 0
		options.Runner.Observe = func(event processpkg.ProcessEvent) {
			switch event.Phase {
			case "start":
				starts++
				live++
				maximum = max(maximum, live)
			case "exit":
				live--
			}
		}
		if err := Render(context.Background(), candidate, options); err != nil {
			t.Fatal(err)
		}
		wantStarts := 2
		if attempt == 1 {
			wantStarts = 1
		}
		if output.String() != "rendered" || starts != wantStarts || live != 0 || maximum != 1 {
			t.Fatalf("attempt=%d output=%q starts=%d live=%d max=%d", attempt, output.String(), starts, live, maximum)
		}
		if attempt == 0 {
			arguments, err := os.ReadFile(converterLog)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(arguments)), "\n")
			if len(lines) != 4 || lines[0] != "-singlefile" || lines[1] != "-jpeg" || lines[2] != path || !filepath.IsAbs(lines[3]) {
				t.Fatalf("converter arguments=%q", lines)
			}
			if err := os.Remove(filepath.Join(tools, "pdftoppm")); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertNoCacheTemps(t, cache.root)
}

func TestConverterFinalValidationRejectsOversizedArtifact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct shell process fixture is Unix-specific")
	}
	tools := t.TempDir()
	script := "#!/bin/sh\n/usr/bin/truncate -s 67108865 \"$4.jpg\"\n"
	if err := os.WriteFile(filepath.Join(tools, "pdftoppm"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	var output bytes.Buffer
	options := testOptions(&output)
	options.Cache, options.Environment = cache, []string{"PATH=" + tools}
	tree := &fakeTreeHandle{}
	options.retainTree = func(*processpkg.Child) (treeHandle, error) { return tree, nil }
	starts, exits := 0, 0
	options.Runner.Observe = func(event processpkg.ProcessEvent) {
		if event.Phase == "start" {
			starts++
		} else if event.Phase == "exit" {
			exits++
		}
	}
	err := Render(context.Background(), resolved(path), options)
	if !errors.Is(err, ErrTerminalResource) || !errors.Is(err, ErrArtifactLimit) || starts != 1 || exits != 1 || tree.kills != 1 || tree.closes != 1 {
		t.Fatalf("err=%v starts=%d exits=%d tree=%+v", err, starts, exits, tree)
	}
	assertNoCacheTemps(t, cache.root)
}

func TestVideoAndAudioConvertersUseExactArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct shell process fixture is Unix-specific")
	}
	cases := []struct {
		name, fixture, tool, artifactArgument string
		artifactIndex                         int
		want                                  func(string, string) []string
	}{
		{name: "video", fixture: "video.mp4", tool: "ffmpegthumbnailer", artifactArgument: "$4", artifactIndex: 3,
			want: func(path, artifact string) []string { return []string{"-i", path, "-o", artifact, "-s", "1080", "-m"} }},
		{name: "audio", fixture: "audio.mp3", tool: "ffmpeg", artifactArgument: "$7", artifactIndex: 6,
			want: func(path, artifact string) []string {
				return []string{"-y", "-i", path, "-an", "-c:v", "copy", artifact}
			}},
	}
	fixtures := writeFixtures(t, t.TempDir())
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tools, log := t.TempDir(), filepath.Join(t.TempDir(), "converter")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nprintf jpeg > \"%s\"\n", strconv.Quote(log), test.artifactArgument)
			if err := os.WriteFile(filepath.Join(tools, test.tool), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tools, "chafa"), []byte("#!/bin/sh\nprintf rendered\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			options := testOptions(&output)
			options.Cache, options.Environment = mustNewCache(t, t.TempDir(), 512<<20), []string{"PATH=" + tools}
			if err := Render(context.Background(), resolved(fixtures[test.fixture]), options); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			arguments := strings.Split(strings.TrimSpace(string(data)), "\n")
			want := test.want(fixtures[test.fixture], arguments[test.artifactIndex])
			if strings.Join(arguments, "\x00") != strings.Join(want, "\x00") || output.String() != "rendered" {
				t.Fatalf("arguments=%q want=%q output=%q", arguments, want, output.String())
			}
		})
	}
}

func TestAudioRichChainNeverStartsMoreThanThreeChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct shell process fixture is Unix-specific")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "ffmpeg"), []byte("#!/bin/sh\nprintf cover > \"$7\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"kitten", "chafa", "exiftool"} {
		if err := os.WriteFile(filepath.Join(tools, tool), []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := writeFixtures(t, t.TempDir())["audio.mp3"]
	var output bytes.Buffer
	options := testOptions(&output)
	options.Cache, options.Environment = mustNewCache(t, t.TempDir(), 512<<20), []string{"PATH=" + tools, "TERM=xterm-kitty"}
	starts := 0
	options.Runner.Observe = func(event processpkg.ProcessEvent) {
		if event.Phase == "start" {
			starts++
		}
	}
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if starts > 3 || !strings.Contains(output.String(), "audio file:") {
		t.Fatalf("starts=%d output=%q", starts, output.String())
	}
}

func TestMissingConverterArtifactFallsThroughAfterWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct shell process fixture is Unix-specific")
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "pdftoppm"), []byte("#!/bin/sh\n/bin/rm \"$4.jpg\"\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := testOptions(&output)
	options.Cache, options.Environment = mustNewCache(t, t.TempDir(), 512<<20), []string{"PATH=" + tools}
	tree := &fakeTreeHandle{}
	options.retainTree = func(*processpkg.Child) (treeHandle, error) { return tree, nil }
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if tree.kills != 0 || tree.closes != 1 || !strings.Contains(output.String(), "PDF document:") {
		t.Fatalf("tree=%+v output=%q", tree, output.String())
	}
}

type zeroReader struct{}

func (zeroReader) Read(data []byte) (int, error) {
	for index := range data {
		data[index] = 0
	}
	return len(data), nil
}

func mustNewCache(t *testing.T, root string, maximum int64) *Cache {
	t.Helper()
	cache, err := NewCache(root, maximum)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}

func mustPut(t *testing.T, cache *Cache, key, value string) string {
	t.Helper()
	path, err := cache.Put(key, strings.NewReader(value))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func makeCacheTemp(t *testing.T, cache *Cache, value string) string {
	t.Helper()
	file, err := os.CreateTemp(cache.root, cacheTempPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Chmod(0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(value); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

func assertNoCacheTemps(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), cacheTempPrefix) {
			t.Fatalf("temporary artifact leaked: %s", entry.Name())
		}
	}
}
