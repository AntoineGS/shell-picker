package preview

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

func renderZip(ctx context.Context, path string, output io.Writer, limits Limits) error {
	if err := preflightZip(path, limits.MaxArchiveEntries); err != nil {
		return err
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	if _, err := fmt.Fprintln(output, "ZIP archive:"); err != nil {
		return err
	}
	var decompressed int64
	for index, entry := range reader.File {
		if index >= limits.MaxArchiveEntries || decompressed >= limits.MaxArchiveDecompressedBytes {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "%4d  %s\n", index+1, escaped(entry.Name)); err != nil {
			return err
		}
		stream, err := entry.Open()
		if err != nil {
			return err
		}
		read, copyErr := copyBounded(ctx, stream, limits.MaxArchiveDecompressedBytes-decompressed)
		closeErr := stream.Close()
		decompressed += read
		if copyErr != nil && !errors.Is(copyErr, ErrInputLimit) {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func preflightZip(path string, maximumEntries int) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	tailSize := min(info.Size(), int64(65_557))
	tail := make([]byte, tailSize)
	if _, err := file.ReadAt(tail, info.Size()-tailSize); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	eocdIndex := bytes.LastIndex(tail, []byte{'P', 'K', 0x05, 0x06})
	if eocdIndex < 0 || len(tail)-eocdIndex < 22 {
		return zip.ErrFormat
	}
	eocd := tail[eocdIndex:]
	if eocdIndex+22+int(binary.LittleEndian.Uint16(eocd[20:22])) != len(tail) {
		return zip.ErrFormat
	}
	entries := uint64(binary.LittleEndian.Uint16(eocd[10:12]))
	centralSize := uint64(binary.LittleEndian.Uint32(eocd[12:16]))
	centralOffset := uint64(binary.LittleEndian.Uint32(eocd[16:20]))
	if entries == 0xffff || centralSize == 0xffffffff || centralOffset == 0xffffffff {
		absoluteEOCD := info.Size() - tailSize + int64(eocdIndex)
		if absoluteEOCD < 20 {
			return zip.ErrFormat
		}
		locator := make([]byte, 20)
		if _, err := file.ReadAt(locator, absoluteEOCD-20); err != nil {
			return err
		}
		if !bytes.Equal(locator[:4], []byte{'P', 'K', 0x06, 0x07}) {
			return zip.ErrFormat
		}
		zip64Offset := int64(binary.LittleEndian.Uint64(locator[8:16]))
		record := make([]byte, 56)
		if _, err := file.ReadAt(record, zip64Offset); err != nil {
			return err
		}
		if !bytes.Equal(record[:4], []byte{'P', 'K', 0x06, 0x06}) {
			return zip.ErrFormat
		}
		entries = binary.LittleEndian.Uint64(record[32:40])
		centralSize = binary.LittleEndian.Uint64(record[40:48])
		centralOffset = binary.LittleEndian.Uint64(record[48:56])
	}
	if entries > uint64(maximumEntries) {
		return ErrArchiveEntries
	}
	return scanZipCentralDirectory(file, info.Size(), centralOffset, centralSize, entries, maximumEntries)
}

func scanZipCentralDirectory(file *os.File, fileSize int64, offset, size, declaredEntries uint64, maximumEntries int) error {
	if offset > uint64(fileSize) || size > uint64(fileSize)-offset {
		return zip.ErrFormat
	}
	cursor, end := int64(offset), int64(offset+size)
	var entries uint64
	header := make([]byte, 46)
	for cursor < end {
		if end-cursor < int64(len(header)) {
			return zip.ErrFormat
		}
		if _, err := file.ReadAt(header, cursor); err != nil {
			return err
		}
		if !bytes.Equal(header[:4], []byte{'P', 'K', 0x01, 0x02}) {
			return zip.ErrFormat
		}
		entries++
		if entries > uint64(maximumEntries) {
			return ErrArchiveEntries
		}
		variable := int64(binary.LittleEndian.Uint16(header[28:30])) +
			int64(binary.LittleEndian.Uint16(header[30:32])) + int64(binary.LittleEndian.Uint16(header[32:34]))
		cursor += int64(len(header)) + variable
		if cursor > end {
			return zip.ErrFormat
		}
	}
	if entries != declaredEntries {
		return zip.ErrFormat
	}
	return nil
}

func renderGzip(ctx context.Context, path string, output io.Writer, limits Limits) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()
	name := reader.Name
	if name == "" {
		name = "(unnamed stream)"
	}
	if _, err := fmt.Fprintf(output, "Gzip archive: %s\n", escaped(name)); err != nil {
		return err
	}
	_, err = copyBounded(ctx, reader, limits.MaxArchiveDecompressedBytes)
	if errors.Is(err, ErrInputLimit) {
		return nil
	}
	return err
}

func renderTar(ctx context.Context, path string, output io.Writer, limits Limits) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := tar.NewReader(file)
	if _, err := fmt.Fprintln(output, "Tar archive:"); err != nil {
		return err
	}
	var decompressed int64
	for entries := 0; entries < limits.MaxArchiveEntries && decompressed < limits.MaxArchiveDecompressedBytes; entries++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "%4d  %s\n", entries+1, escaped(header.Name)); err != nil {
			return err
		}
		read, err := copyBounded(ctx, reader, limits.MaxArchiveDecompressedBytes-decompressed)
		decompressed += read
		if err != nil && !errors.Is(err, ErrInputLimit) {
			return err
		}
	}
	return nil
}

func copyBounded(ctx context.Context, source io.Reader, maximum int64) (int64, error) {
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		remaining := maximum - total
		if remaining <= 0 {
			return total, ErrInputLimit
		}
		if int64(len(buffer)) > remaining {
			buffer = buffer[:remaining]
		}
		read, err := source.Read(buffer)
		total += int64(read)
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}
