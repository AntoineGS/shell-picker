package preview

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
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

func TestOptionalArchiveListingStopsAtOneHundredLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct shell process fixture is Unix-specific")
	}
	tools := t.TempDir()
	script := "#!/bin/sh\ni=0; while [ $i -lt 150 ]; do printf 'entry-%03d\\n' $i; i=$((i+1)); done\n"
	if err := os.WriteFile(filepath.Join(tools, "unzip"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sample.zip")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	entry, err := writer.Create("one")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("one"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	options := testOptions(&output)
	options.Environment = []string{"PATH=" + tools}
	if err := Render(context.Background(), resolved(path), options); err != nil {
		t.Fatal(err)
	}
	if lines := bytes.Count(output.Bytes(), []byte("\n")); lines != 100 {
		t.Fatalf("lines=%d output bytes=%d", lines, output.Len())
	}
}

func TestNativeZipAndTarListingsContainSizesWithinOneHundredLines(t *testing.T) {
	root := t.TempDir()
	zipPath, tarPath := filepath.Join(root, "sample.zip"), filepath.Join(root, "sample.tar")
	var zipped bytes.Buffer
	zipWriter := zip.NewWriter(&zipped)
	for index := 0; index < 100; index++ {
		entry, err := zipWriter.Create(fmt.Sprintf("zip-%03d", index))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = entry.Write([]byte("abc"))
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zipPath, zipped.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	tarWriter := tar.NewWriter(file)
	for index := 0; index < 100; index++ {
		name := fmt.Sprintf("tar-%03d", index)
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: 3}); err != nil {
			t.Fatal(err)
		}
		_, _ = tarWriter.Write([]byte("abc"))
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path   string
		render func(context.Context, string, io.Writer, Limits) error
	}{{zipPath, renderZip}, {tarPath, renderTar}} {
		var output bytes.Buffer
		if err := test.render(context.Background(), test.path, &output, DefaultLimits); err != nil {
			t.Fatal(err)
		}
		if lines := bytes.Count(output.Bytes(), []byte("\n")); lines > 100 || !strings.Contains(output.String(), "3  ") {
			t.Fatalf("path=%s lines=%d output prefix=%q", test.path, lines, output.String()[:min(output.Len(), 80)])
		}
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

func TestZipPreflightScansArchiveZipEffectiveShiftedDirectory(t *testing.T) {
	data := shiftedZipWithRawOffsetDecoy(t, DefaultLimits.MaxArchiveEntries+1)
	path := filepath.Join(t.TempDir(), "shifted-decoy.zip")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
		t.Fatal("archive/zip accepted forged shifted directory count")
	}
	if err := preflightZip(path, DefaultLimits.MaxArchiveEntries); !errors.Is(err, ErrArchiveEntries) {
		t.Fatalf("preflight error=%v", err)
	}
}

func shiftedZipWithRawOffsetDecoy(t *testing.T, entries int) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for index := 0; index < entries; index++ {
		if _, err := writer.Create(fmt.Sprintf("real-entry-%03d", index)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	zipData := archive.Bytes()
	eocd := bytes.LastIndex(zipData, []byte{'P', 'K', 0x05, 0x06})
	if eocd < 0 {
		t.Fatal("missing EOCD")
	}
	centralSize := int(binary.LittleEndian.Uint32(zipData[eocd+12 : eocd+16]))
	if centralSize <= 50 || centralSize-46 > 0xffff {
		t.Fatalf("unexpected central size %d", centralSize)
	}
	decoy := make([]byte, centralSize)
	copy(decoy[:4], []byte{'P', 'K', 0x01, 0x02})
	binary.LittleEndian.PutUint32(decoy[20:24], 0xffffffff)
	binary.LittleEndian.PutUint16(decoy[30:32], uint16(centralSize-46))
	binary.LittleEndian.PutUint16(decoy[46:48], 0x0001)
	binary.LittleEndian.PutUint16(decoy[48:50], 0)
	binary.LittleEndian.PutUint16(zipData[eocd+8:eocd+10], 1)
	binary.LittleEndian.PutUint16(zipData[eocd+10:eocd+12], 1)
	binary.LittleEndian.PutUint32(zipData[eocd+16:eocd+20], 0)
	return append(decoy, zipData...)
}

func TestZipPreflightMatchesGoDirectorySizeZIP64Sentinel(t *testing.T) {
	data := zip64DirectorySizeSentinelWithDecoy(t, DefaultLimits.MaxArchiveEntries+1)
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("archive/zip: %v", err)
	}
	if len(reader.File) != DefaultLimits.MaxArchiveEntries+1 {
		t.Fatalf("archive/zip files=%d", len(reader.File))
	}
	path := filepath.Join(t.TempDir(), "directory-size-sentinel.zip")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightZip(path, DefaultLimits.MaxArchiveEntries); !errors.Is(err, ErrArchiveEntries) {
		t.Fatalf("preflight error=%v", err)
	}
}

func zip64DirectorySizeSentinelWithDecoy(t *testing.T, entries int) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for index := 0; index < entries; index++ {
		if _, err := writer.Create(fmt.Sprintf("real-entry-%03d", index)); err != nil {
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
	centralSize := binary.LittleEndian.Uint32(data[eocd+12 : eocd+16])
	centralOffset := binary.LittleEndian.Uint32(data[eocd+16 : eocd+20])
	result := append([]byte(nil), data[:eocd]...)
	padding := make([]byte, 65_459)
	copy(padding[:4], []byte{'P', 'K', 0x01, 0x02})
	binary.LittleEndian.PutUint32(padding[20:24], 0xffffffff)
	binary.LittleEndian.PutUint16(padding[30:32], 4)
	binary.LittleEndian.PutUint16(padding[46:48], 0x0001)
	result = append(result, padding...)
	zip64Offset := uint64(len(result))
	zip64End := make([]byte, 56)
	copy(zip64End[:4], []byte{'P', 'K', 0x06, 0x06})
	binary.LittleEndian.PutUint64(zip64End[4:12], 44)
	binary.LittleEndian.PutUint16(zip64End[12:14], 45)
	binary.LittleEndian.PutUint16(zip64End[14:16], 45)
	binary.LittleEndian.PutUint64(zip64End[24:32], uint64(entries))
	binary.LittleEndian.PutUint64(zip64End[32:40], uint64(entries))
	binary.LittleEndian.PutUint64(zip64End[40:48], uint64(centralSize))
	binary.LittleEndian.PutUint64(zip64End[48:56], uint64(centralOffset))
	result = append(result, zip64End...)
	locator := make([]byte, 20)
	copy(locator[:4], []byte{'P', 'K', 0x06, 0x07})
	binary.LittleEndian.PutUint64(locator[8:16], zip64Offset)
	binary.LittleEndian.PutUint32(locator[16:20], 1)
	result = append(result, locator...)
	legacyEnd := make([]byte, 22)
	copy(legacyEnd[:4], []byte{'P', 'K', 0x05, 0x06})
	binary.LittleEndian.PutUint16(legacyEnd[8:10], 1)
	binary.LittleEndian.PutUint16(legacyEnd[10:12], 1)
	binary.LittleEndian.PutUint32(legacyEnd[12:16], 0xffff)
	return append(result, legacyEnd...)
}
