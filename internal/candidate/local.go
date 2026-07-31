package candidate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const localBatchSize = 128

type LocalOptions struct {
	StatWorkers int
}

type directoryReader interface {
	ReadDir(int) ([]os.DirEntry, error)
	Close() error
}

var (
	openLocalDirectory = func(path string) (directoryReader, error) { return os.Open(path) }
	statLocalPath      = os.Stat
)

type localEntry struct {
	name      []byte
	folded    []byte
	path      []byte
	hidden    bool
	directory bool
	symlink   bool
}

func EnumerateLocal(ctx context.Context, picker protocol.Picker, location pathutil.Location, options LocalOptions) ([]Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if picker != protocol.PickerCD && picker != protocol.PickerCP {
		return nil, fmt.Errorf("unsupported picker %q", picker)
	}

	switch location.Kind {
	case pathutil.KindFilesystem:
		return enumerateFilesystem(ctx, picker, location, options)
	case pathutil.KindDrives:
		return enumerateDrives(ctx)
	default:
		return nil, fmt.Errorf("unsupported location kind %d", location.Kind)
	}
}

func enumerateFilesystem(ctx context.Context, picker protocol.Picker, location pathutil.Location, options LocalOptions) (records []Record, err error) {
	if !filepath.IsAbs(string(location.Path)) {
		return nil, fmt.Errorf("local path %q is not absolute", location.Path)
	}
	directory, err := openLocalDirectory(string(location.Path))
	if err != nil {
		return nil, fmt.Errorf("open local directory %q: %w", location.Path, err)
	}
	defer func() {
		if closeErr := directory.Close(); closeErr != nil {
			records = nil
			err = errors.Join(err, fmt.Errorf("close local directory %q: %w", location.Path, closeErr))
		}
	}()

	entries, err := readLocalEntries(ctx, directory, location.Path)
	if err != nil {
		return nil, err
	}
	if err := classifySymlinks(ctx, entries, options.StatWorkers); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return buildLocalRecords(picker, location, entries), nil
}

func readLocalEntries(ctx context.Context, directory directoryReader, base []byte) ([]localEntry, error) {
	entries := make([]localEntry, 0, localBatchSize)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, readErr := directory.ReadDir(localBatchSize)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("read local directory %q: %w", base, readErr)
		}
		for _, directoryEntry := range batch {
			name := []byte(directoryEntry.Name())
			if bytes.IndexByte(name, 0) >= 0 {
				return nil, fmt.Errorf("local entry name contains NUL: %q", name)
			}
			path := []byte(filepath.Join(string(base), directoryEntry.Name()))
			entries = append(entries, localEntry{
				name:      name,
				folded:    foldBytes(name),
				path:      path,
				hidden:    len(name) > 0 && name[0] == '.',
				directory: directoryEntry.IsDir(),
				symlink:   directoryEntry.Type()&os.ModeSymlink != 0,
			})
		}
		if errors.Is(readErr, io.EOF) {
			return entries, nil
		}
	}
}

func classifySymlinks(ctx context.Context, entries []localEntry, requestedWorkers int) error {
	symlinkIndices := make([]int, 0)
	for index := range entries {
		if entries[index].symlink {
			symlinkIndices = append(symlinkIndices, index)
		}
	}
	if len(symlinkIndices) == 0 {
		return ctx.Err()
	}

	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	errorsFound := make(chan error, 1)
	var workers sync.WaitGroup
	for range localWorkerCount(requestedWorkers) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-workerCtx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					info, err := statLocalPath(string(entries[index].path))
					if err != nil {
						if errors.Is(err, os.ErrNotExist) {
							continue
						}
						select {
						case errorsFound <- fmt.Errorf("stat local entry %q: %w", entries[index].path, err):
							cancel()
						default:
						}
						return
					}
					entries[index].directory = info.IsDir()
				}
			}
		}()
	}

sendJobs:
	for _, index := range symlinkIndices {
		select {
		case jobs <- index:
		case <-workerCtx.Done():
			break sendJobs
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-errorsFound:
		return err
	default:
		return ctx.Err()
	}
}

func localWorkerCount(requested int) int {
	if requested <= 0 {
		requested = runtime.GOMAXPROCS(0)
	}
	if requested < 2 {
		return 2
	}
	if requested > 8 {
		return 8
	}
	return requested
}

func buildLocalRecords(picker protocol.Picker, location pathutil.Location, entries []localEntry) []Record {
	directories := make([]localEntry, 0, len(entries))
	files := make([]localEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.directory {
			directories = append(directories, entry)
		} else if picker == protocol.PickerCP {
			files = append(files, entry)
		}
	}
	sortLocalEntries(directories)
	sortLocalEntries(files)

	directoryKind := localDirectoryKind(picker)
	records := rootRecords(picker, location)
	if needed := len(records) + len(directories) + len(files); cap(records) < needed {
		grown := make([]Record, len(records), needed)
		copy(grown, records)
		records = grown
	}
	for _, entry := range directories {
		display := protocol.EscapeDisplay(entry.name)
		if picker == protocol.PickerCP {
			display += "/"
		}
		records = append(records, newRecord(directoryKind, display, entry.path))
	}
	for _, entry := range files {
		records = append(records, newRecord(protocol.KindFile, protocol.EscapeDisplay(entry.name), entry.path))
	}
	return records
}

func ordinaryRootRecords(picker protocol.Picker, location pathutil.Location) []Record {
	kind := localDirectoryKind(picker)
	return []Record{
		newRecord(kind, ".", location.Path),
		newRecord(kind, "..", pathutil.Parent(location).Path),
	}
}

func localDirectoryKind(picker protocol.Picker) protocol.Kind {
	if picker == protocol.PickerCP {
		return protocol.KindDirectory
	}
	return protocol.KindLocal
}

func sortLocalEntries(entries []localEntry) {
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].hidden != entries[right].hidden {
			return entries[left].hidden
		}
		if compared := bytes.Compare(entries[left].folded, entries[right].folded); compared != 0 {
			return compared < 0
		}
		return bytes.Compare(entries[left].name, entries[right].name) < 0
	})
}

func lessFolded(left, right []byte) bool {
	foldedLeft := foldBytes(left)
	foldedRight := foldBytes(right)
	if compared := bytes.Compare(foldedLeft, foldedRight); compared != 0 {
		return compared < 0
	}
	return bytes.Compare(left, right) < 0
}

func foldBytes(value []byte) []byte {
	folded := make([]byte, 0, len(value))
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		if r == utf8.RuneError && size == 1 {
			folded = append(folded, value[0])
			value = value[1:]
			continue
		}
		folded = utf8.AppendRune(folded, unicode.ToLower(r))
		value = value[size:]
	}
	return folded
}
