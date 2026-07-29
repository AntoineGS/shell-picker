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
func TestNewCacheDefaultFailsWithoutCacheVariableOrHome(t *testing.T) {
	oldGetenv, oldHome := cacheGetenv, cacheUserHome
	cacheGetenv = func(string) string { return "" }
	cacheUserHome = func() (string, error) { return "", errors.New("missing") }
	t.Cleanup(func() { cacheGetenv, cacheUserHome = oldGetenv, oldHome })
	if _, err := NewCache("", 1); err == nil {
		t.Fatal("NewCache accepted missing default root")
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
	maximum := int64(^uint64(0) >> 1)
	if total := saturatedAdd(maximum-1, 8); total != maximum {
		t.Fatalf("saturated total=%d", total)
	}
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
func TestCachePutAnchorsRootAcrossSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privilege and Unix race fixture")
	}
	parent, outside := t.TempDir(), t.TempDir()
	root := filepath.Join(parent, "cache")
	cache := mustNewCache(t, root, 512<<20)
	reader := newBarrierReader("safe")
	done := make(chan error, 1)
	key := strings.Repeat("c", 64)
	go func() { _, err := cache.Put(key, reader); done <- err }()
	<-reader.started
	oldRoot := root + "-old"
	mustRenameAndSymlink(t, root, oldRoot, outside)
	close(reader.proceed)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(oldRoot, key))
	entries, outsideErr := os.ReadDir(outside)
	if err != nil || string(data) != "safe" || outsideErr != nil || len(entries) != 0 {
		t.Fatalf("winner=%q err=%v outside=%v outsideErr=%v", data, err, entries, outsideErr)
	}
}
func TestCacheOperationsRejectOrdinaryDirectoryRootReplacement(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "cache")
	cache := mustNewCache(t, root, 512<<20)
	if err := errors.Join(os.Rename(root, root+"-original"), os.Mkdir(root, 0o700)); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("9", 64)
	if _, err := cache.Put(key, strings.NewReader("unsafe")); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("Put error=%v", err)
	}
	if _, ok := cache.Get(key); ok {
		t.Fatal("Get accepted replacement cache root")
	}
	if err := cache.Prune(); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("Prune error=%v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("replacement entries=%v err=%v", entries, err)
	}
}
func TestCachePutRejectsHardlinkedTempAndWinner(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	key := strings.Repeat("d", 64)
	attacker := filepath.Join(t.TempDir(), "attacker")
	if err := os.WriteFile(attacker, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(attacker, filepath.Join(cache.root, key)); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Put(key, strings.NewReader("safe")); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("winner err=%v", err)
	}
	symlinkKey := strings.Repeat("b", 64)
	if err := os.Symlink(attacker, filepath.Join(cache.root, symlinkKey)); err == nil {
		if _, err := cache.Put(symlinkKey, strings.NewReader("safe")); !errors.Is(err, ErrUnsafeCache) {
			t.Fatalf("symlink winner err=%v", err)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	key = strings.Repeat("e", 64)
	reader := newBarrierReader("safe")
	done := make(chan error, 1)
	go func() { _, err := cache.Put(key, reader); done <- err }()
	<-reader.started
	temp := soleCacheTemp(t, cache.root)
	if err := os.Link(temp, filepath.Join(cache.root, "attacker-link")); err != nil {
		t.Fatal(err)
	}
	close(reader.proceed)
	if err := <-done; !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("temp err=%v", err)
	}
}
func TestCacheConcurrentProductionPutHasImmutableSingleLinkWinner(t *testing.T) {
	cache := mustNewCache(t, t.TempDir(), 512<<20)
	key := strings.Repeat("f", 64)
	readers := []*barrierReader{newBarrierReader("first"), newBarrierReader("second")}
	errs := make(chan error, 2)
	for _, reader := range readers {
		go func(r io.Reader) { _, err := cache.Put(key, r); errs <- err }(reader)
	}
	for _, reader := range readers {
		<-reader.started
	}
	for _, reader := range readers {
		close(reader.proceed)
	}
	for range readers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(cache.root, key))
	if err != nil || string(data) != "first" && string(data) != "second" {
		t.Fatalf("winner=%q err=%v", data, err)
	}
	probe := filepath.Join(cache.root, "winner-hardlink")
	if err := os.Link(filepath.Join(cache.root, key), probe); err == nil {
		if _, ok := cache.Get(key); ok {
			t.Fatal("Get accepted winner after link count changed")
		}
		_ = os.Remove(probe)
	}
	assertNoCacheTemps(t, cache.root)
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
			renderCall, err := os.ReadFile(rendererLog)
			if err != nil {
				t.Fatal(err)
			}
			renderArguments := strings.Split(strings.TrimSpace(string(renderCall)), "\n")
			if renderArguments[len(renderArguments)-1] == lines[3]+".jpg" {
				t.Fatalf("renderer received original converter artifact: %q", renderArguments)
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
	clear(data)
	return len(data), nil
}

type barrierReader struct {
	reader           *strings.Reader
	started, proceed chan struct{}
	once             sync.Once
}

func newBarrierReader(value string) *barrierReader {
	return &barrierReader{reader: strings.NewReader(value), started: make(chan struct{}), proceed: make(chan struct{})}
}
func (reader *barrierReader) Read(data []byte) (int, error) {
	reader.once.Do(func() { close(reader.started); <-reader.proceed })
	return reader.reader.Read(data)
}
func mustRenameAndSymlink(t *testing.T, root, oldRoot, outside string) {
	t.Helper()
	if err := os.Rename(root, oldRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
}
func soleCacheTemp(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), cacheTempPrefix) {
			return filepath.Join(root, entry.Name())
		}
	}
	t.Fatal("cache temp not found")
	return ""
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
