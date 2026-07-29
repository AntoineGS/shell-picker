package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveLimitsEntriesBytesAndDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bomb.zip")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	for index := 0; index < DefaultLimits.MaxArchiveEntries; index++ {
		entry, err := zw.Create(fmt.Sprintf("entry-%03d-%s", index, strings.Repeat("x", 32)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte(strings.Repeat("z", 48<<10)))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	options := testOptions(&output)
	started := time.Now()
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(output.Bytes(), []byte("\n")); lines > DefaultLimits.MaxArchiveEntries+2 {
		t.Fatalf("listed %d lines", lines)
	}
	if output.Len() > int(DefaultLimits.MaxOutputBytes) || time.Since(started) > DefaultLimits.Deadline {
		t.Fatalf("bytes=%d duration=%s", output.Len(), time.Since(started))
	}

	output.Reset()
	options.Limits.Deadline = time.Nanosecond
	if err := Render(context.Background(), resolved(path), options); err != context.DeadlineExceeded {
		t.Fatalf("deadline err=%v", err)
	}
}

func TestZipPreflightRejectsCentralDirectoryBombBeforeArchiveOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "central-directory-bomb.zip")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for index := 0; index < DefaultLimits.MaxArchiveEntries+1; index++ {
		entry, err := writer.Create(fmt.Sprintf("entry-%03d", index))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte("x"))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightZip(path, DefaultLimits.MaxArchiveEntries); !errors.Is(err, ErrArchiveEntries) {
		t.Fatalf("preflight error=%v", err)
	}
}

func TestMalformedExtensionClassifiedArchivesEmitUsefulFallback(t *testing.T) {
	for _, extension := range []string{".zip", ".gz", ".tar"} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "malformed"+extension)
			if err := os.WriteFile(path, []byte("not an archive"), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := Render(context.Background(), resolved(path), testOptions(&output)); err != nil {
				t.Fatalf("Render: %v", err)
			}
			if output.Len() == 0 {
				t.Fatal("blank malformed archive fallback")
			}
		})
	}
}

func TestZip64PreflightRejectsEntryCountBeforeArchiveOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zip64-central-directory-bomb.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for index := 0; index < 1<<16; index++ {
		if _, err := writer.Create(fmt.Sprintf("entry-%05d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := preflightZip(path, DefaultLimits.MaxArchiveEntries); !errors.Is(err, ErrArchiveEntries) {
		t.Fatalf("preflight error=%v", err)
	}
}

func TestZipPreflightCountsCentralHeadersInsteadOfTrustingEOCD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forged-count.zip")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for index := 0; index < DefaultLimits.MaxArchiveEntries+1; index++ {
		if _, err := writer.Create(fmt.Sprintf("entry-%03d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := archive.Bytes()
	eocd := bytes.LastIndex(data, []byte{'P', 'K', 0x05, 0x06})
	if eocd < 0 {
		t.Fatal("missing EOCD")
	}
	binary.LittleEndian.PutUint16(data[eocd+8:eocd+10], 1)
	binary.LittleEndian.PutUint16(data[eocd+10:eocd+12], 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightZip(path, DefaultLimits.MaxArchiveEntries); !errors.Is(err, ErrArchiveEntries) {
		t.Fatalf("preflight error=%v", err)
	}
}
