package preview

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZipPreflightMatchesGoDirectorySizeZIP64Sentinel(t *testing.T) {
	acceptedMarkers := 0
	for _, marker := range []uint32{0xffff, 0xffffffff} {
		t.Run(fmt.Sprintf("%x", marker), func(t *testing.T) {
			data := zip64DirectorySizeSentinelWithDecoy(t, DefaultLimits.MaxArchiveEntries+1)
			binary.LittleEndian.PutUint32(data[len(data)-10:len(data)-6], marker)
			reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				if !errors.Is(err, zip.ErrFormat) {
					t.Fatalf("archive/zip: %v", err)
				}
				// Preflight bounds entries; archive/zip remains responsible
				// for rejecting layouts this toolchain does not interpret.
				return
			}
			acceptedMarkers++
			if len(reader.File) != DefaultLimits.MaxArchiveEntries+1 {
				t.Fatalf("archive/zip files=%d", len(reader.File))
			}
			path := writeZipPreflightFixture(t, data)
			if err := preflightZip(path, DefaultLimits.MaxArchiveEntries); !errors.Is(err, ErrArchiveEntries) {
				t.Fatalf("preflight error=%v, want %v", err, ErrArchiveEntries)
			}
		})
	}
	if acceptedMarkers == 0 {
		t.Fatal("neither marker exercised archive/zip's ZIP64 reader")
	}
}

func TestZipPreflightOrdinaryDirectorySize65535(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	// One central header is 46 bytes, followed by the filename.
	if _, err := writer.Create(strings.Repeat("a", 0xffff-46)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	if got := binary.LittleEndian.Uint32(data[len(data)-10 : len(data)-6]); got != 0xffff {
		t.Fatalf("central directory size=%d", got)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 1 {
		t.Fatalf("ordinary ZIP rejected by archive/zip: %v", err)
	}
	if err := preflightZip(writeZipPreflightFixture(t, data), 1); err != nil {
		t.Fatalf("ordinary 65535-byte central directory: %v", err)
	}
}

func TestZipPreflightRejectsMalformedZIP64DirectoryMetadata(t *testing.T) {
	for _, marker := range []uint32{0xffff, 0xffffffff} {
		for _, corruption := range []string{"missing-locator", "multiple-disks", "offset-overflow", "bad-record"} {
			t.Run(fmt.Sprintf("%x/%s", marker, corruption), func(t *testing.T) {
				data := zip64DirectorySizeSentinelWithDecoy(t, 2)
				binary.LittleEndian.PutUint32(data[len(data)-10:len(data)-6], marker)
				locator := data[len(data)-42 : len(data)-22]
				recordOffset := int(binary.LittleEndian.Uint64(locator[8:16]))
				switch corruption {
				case "missing-locator":
					locator[0] = 0
				case "multiple-disks":
					binary.LittleEndian.PutUint32(locator[16:20], 2)
				case "offset-overflow":
					binary.LittleEndian.PutUint64(locator[8:16], ^uint64(0))
				case "bad-record":
					data[recordOffset] = 0
				}
				if err := preflightZip(writeZipPreflightFixture(t, data), 100); err == nil {
					if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
						t.Fatal("preflight and archive/zip accepted malformed ZIP64 directory metadata")
					}
				}
			})
		}
	}
}

func writeZipPreflightFixture(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
