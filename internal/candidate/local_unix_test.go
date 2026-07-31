//go:build !windows

package candidate

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestEnumerateCPOrderAndIdentity(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, root, ".hidden-dir")
	mustMkdir(t, root, "VisibleDir")
	mustWrite(t, root, []byte(".hidden-file"))
	mustWrite(t, root, []byte("visible file"))
	mustMkdir(t, root, "ignored")
	mustMkdir(t, root, "link-target")
	if err := os.Symlink(filepath.Join(root, "link-target"), filepath.Join(root, "link-dir")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, root, []byte{'b', 'a', 'd', '-', 0xff})

	records, err := EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte(root)), LocalOptions{StatWorkers: 4})
	if err != nil {
		t.Fatal(err)
	}
	assertDisplays(t, records, []string{
		".", "..", ".hidden-dir/", "ignored/", "link-dir/", "link-target/", "VisibleDir/",
		".hidden-file", `bad-\xFF`, "visible file",
	})
	for _, record := range records {
		decoded, decodeErr := protocol.DecodePath(record.Wire().Payload)
		if decodeErr != nil || !bytes.Equal(decoded, record.Path) {
			t.Fatalf("record=%+v err=%v", record, decodeErr)
		}
		if record.FullKey() != string(record.Wire().Bytes()) {
			t.Fatalf("FullKey() = %q; want exact wire record %q", record.FullKey(), record.Wire().Bytes())
		}
	}
}

func TestEnumerateCDIncludesIgnoredDirectoriesAndNoFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".git", ".worktrees", "visible"} {
		mustMkdir(t, root, name)
	}
	mustWrite(t, root, []byte("file"))

	records, err := EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Filesystem([]byte(root)), LocalOptions{StatWorkers: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertDisplays(t, records, []string{".", "..", ".git", ".worktrees", "visible"})
	for _, record := range records {
		if record.Kind != protocol.KindLocal {
			t.Errorf("kind for %q = %q; want %q", record.Display, record.Kind, protocol.KindLocal)
		}
	}
}

func TestEnumerateCDSkipsDanglingSymlink(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, root, "visible")
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}

	records, err := EnumerateLocal(context.Background(), protocol.PickerCD, pathutil.Filesystem([]byte(root)), LocalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertDisplays(t, records, []string{".", "..", "visible"})
}

func TestDeterministicFoldedOrderUsesRawByteTie(t *testing.T) {
	names := [][]byte{[]byte("a"), []byte("A"), []byte("ä"), []byte("Ä"), {'a', 0xff}, {'A', 0xff}}
	sort.Slice(names, func(i, j int) bool { return lessFolded(names[i], names[j]) })
	want := [][]byte{[]byte("A"), []byte("a"), {'A', 0xff}, {'a', 0xff}, []byte("Ä"), []byte("ä")}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("ordered names = %q; want %q", names, want)
	}
}

func TestEnumerateReadsExactBatchesAndRejectsNUL(t *testing.T) {
	reader := &fakeDirectory{reads: []fakeRead{
		{entries: []os.DirEntry{fakeDirEntry{name: "ok"}}},
		{entries: []os.DirEntry{fakeDirEntry{name: "bad\x00name"}}},
	}}
	restoreOpen := replaceOpenLocal(t, func(string) (directoryReader, error) { return reader, nil })
	defer restoreOpen()

	records, err := EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte("/absolute")), LocalOptions{})
	if err == nil {
		t.Fatal("expected NUL-name error")
	}
	if records != nil {
		t.Fatalf("records = %+v; want no partial publication", records)
	}
	if !reflect.DeepEqual(reader.sizes, []int{128, 128}) {
		t.Fatalf("ReadDir sizes = %v; want [128 128]", reader.sizes)
	}
}

func TestEnumerateReadDirErrorPublishesNothing(t *testing.T) {
	wantErr := errors.New("read failed")
	reader := &fakeDirectory{reads: []fakeRead{
		{entries: []os.DirEntry{fakeDirEntry{name: "first"}}},
		{err: wantErr},
	}}
	restoreOpen := replaceOpenLocal(t, func(string) (directoryReader, error) { return reader, nil })
	defer restoreOpen()

	records, err := EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte("/absolute")), LocalOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v; want wrapped %v", err, wantErr)
	}
	if records != nil {
		t.Fatalf("records = %+v; want no partial publication", records)
	}
}

func TestEnumerateStatErrorPublishesNothing(t *testing.T) {
	wantErr := errors.New("stat failed")
	reader := symlinkDirectory(3)
	restoreOpen := replaceOpenLocal(t, func(string) (directoryReader, error) { return reader, nil })
	defer restoreOpen()
	restoreStat := replaceStatLocal(t, func(string) (os.FileInfo, error) { return nil, wantErr })
	defer restoreStat()

	records, err := EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte("/absolute")), LocalOptions{StatWorkers: 2})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v; want wrapped %v", err, wantErr)
	}
	if records != nil {
		t.Fatalf("records = %+v; want no partial publication", records)
	}
}

func TestStatWorkerBound(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested int
		wantLive  int
	}{
		{name: "minimum", requested: 1, wantLive: 2},
		{name: "maximum", requested: 99, wantLive: 8},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := symlinkDirectory(32)
			restoreOpen := replaceOpenLocal(t, func(string) (directoryReader, error) { return reader, nil })
			defer restoreOpen()

			entered := make(chan struct{}, 32)
			release := make(chan struct{})
			var active atomic.Int32
			var maximum atomic.Int32
			restoreStat := replaceStatLocal(t, func(name string) (os.FileInfo, error) {
				live := active.Add(1)
				for old := maximum.Load(); live > old && !maximum.CompareAndSwap(old, live); old = maximum.Load() {
				}
				entered <- struct{}{}
				<-release
				active.Add(-1)
				return fakeFileInfo{name: filepath.Base(name), directory: true}, nil
			})
			defer restoreStat()

			done := make(chan error, 1)
			go func() {
				_, err := EnumerateLocal(context.Background(), protocol.PickerCP, pathutil.Filesystem([]byte("/absolute")), LocalOptions{StatWorkers: test.requested})
				done <- err
			}()
			for range test.wantLive {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for stat workers")
				}
			}
			select {
			case <-entered:
				t.Fatalf("more than %d Stat calls entered concurrently", test.wantLive)
			default:
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if got := int(maximum.Load()); got != test.wantLive {
				t.Fatalf("maximum live Stat calls = %d; want %d", got, test.wantLive)
			}
		})
	}
}

func TestEnumerateHonorsCancellationBeforeOpening(t *testing.T) {
	called := false
	restoreOpen := replaceOpenLocal(t, func(string) (directoryReader, error) {
		called = true
		return nil, errors.New("must not open")
	})
	defer restoreOpen()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	records, err := EnumerateLocal(ctx, protocol.PickerCP, pathutil.Filesystem([]byte("/absolute")), LocalOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want context cancellation", err)
	}
	if records != nil || called {
		t.Fatalf("records=%+v open-called=%v; want neither", records, called)
	}
}

func BenchmarkEnumerateLocal256(b *testing.B) {
	root := b.TempDir()
	for index := range 128 {
		mustMkdir(b, root, "dir-"+string(rune(0x100+index)))
		mustWrite(b, root, []byte("file-"+string(rune(0x100+index))))
	}
	location := pathutil.Filesystem([]byte(root))
	b.ResetTimer()
	for range b.N {
		if _, err := EnumerateLocal(context.Background(), protocol.PickerCP, location, LocalOptions{StatWorkers: 4}); err != nil {
			b.Fatal(err)
		}
	}
}

type testLogger interface {
	Helper()
	Fatal(...any)
}

func mustMkdir(t testLogger, root, name string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t testLogger, root string, name []byte) {
	t.Helper()
	path := append(append([]byte(root), filepath.Separator), name...)
	if err := os.WriteFile(string(path), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertDisplays(t *testing.T, records []Record, want []string) {
	t.Helper()
	got := make([]string, len(records))
	for index := range records {
		got[index] = records[index].Display
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("displays = %q; want %q", got, want)
	}
}

type fakeRead struct {
	entries []os.DirEntry
	err     error
}

type fakeDirectory struct {
	reads []fakeRead
	sizes []int
}

func (directory *fakeDirectory) ReadDir(size int) ([]os.DirEntry, error) {
	directory.sizes = append(directory.sizes, size)
	if len(directory.reads) == 0 {
		return nil, io.EOF
	}
	read := directory.reads[0]
	directory.reads = directory.reads[1:]
	return read.entries, read.err
}

func (*fakeDirectory) Close() error { return nil }

type fakeDirEntry struct {
	name      string
	directory bool
	symlink   bool
}

func (entry fakeDirEntry) Name() string { return entry.name }
func (entry fakeDirEntry) IsDir() bool  { return entry.directory }
func (entry fakeDirEntry) Info() (os.FileInfo, error) {
	return fakeFileInfo{name: entry.name, directory: entry.directory}, nil
}
func (entry fakeDirEntry) Type() fs.FileMode {
	if entry.symlink {
		return os.ModeSymlink
	}
	if entry.directory {
		return os.ModeDir
	}
	return 0
}

type fakeFileInfo struct {
	name      string
	directory bool
}

func (info fakeFileInfo) Name() string { return info.name }
func (fakeFileInfo) Size() int64       { return 0 }
func (info fakeFileInfo) Mode() fs.FileMode {
	if info.directory {
		return os.ModeDir
	}
	return 0
}
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info fakeFileInfo) IsDir() bool   { return info.directory }
func (fakeFileInfo) Sys() any           { return nil }

func symlinkDirectory(count int) *fakeDirectory {
	entries := make([]os.DirEntry, count)
	for index := range entries {
		entries[index] = fakeDirEntry{name: string(rune('a' + index)), symlink: true}
	}
	return &fakeDirectory{reads: []fakeRead{{entries: entries}, {err: io.EOF}}}
}

func replaceOpenLocal(t *testing.T, replacement func(string) (directoryReader, error)) func() {
	t.Helper()
	original := openLocalDirectory
	openLocalDirectory = replacement
	return func() { openLocalDirectory = original }
}

func replaceStatLocal(t *testing.T, replacement func(string) (os.FileInfo, error)) func() {
	t.Helper()
	original := statLocalPath
	statLocalPath = replacement
	return func() { statLocalPath = original }
}
