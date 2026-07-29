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

type lineLimitWriter struct {
	writer    io.Writer
	remaining int
}

func archiveCategory(category Category) bool {
	return category == CategoryZip || category == CategoryGzip || category == CategoryXz || category == CategoryTar || category == CategoryBzip
}

func renderArchiveOrFallback(ctx context.Context, category Category, info os.FileInfo, output io.Writer, render func() error) error {
	err := render()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, ErrOutputLimit) || errors.Is(err, ErrInputLimit) || errors.Is(err, ErrArchiveEntries) {
		return err
	}
	return renderMetadata(category, info, output)
}

func (writer *lineLimitWriter) Write(data []byte) (int, error) {
	if writer.remaining <= 0 {
		return 0, ErrArchiveEntries
	}
	allowed := len(data)
	for index, value := range data {
		if value == '\n' {
			writer.remaining--
			if writer.remaining == 0 {
				allowed = index + 1
				break
			}
		}
	}
	written, err := writer.writer.Write(data[:allowed])
	if err == nil && allowed < len(data) {
		err = ErrArchiveEntries
	}
	return written, err
}

func renderZip(ctx context.Context, path string, output io.Writer, limits Limits) error {
	if err := preflightZip(path, limits.MaxArchiveEntries); err != nil {
		return err
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	var decompressed int64
	for index, entry := range reader.File {
		if index >= limits.MaxArchiveEntries || decompressed >= limits.MaxArchiveDecompressedBytes {
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "%4d  %10d  %s\n", index+1, entry.UncompressedSize64, escaped(entry.Name)); err != nil {
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
	directoryEndOffset := info.Size() - tailSize + int64(eocdIndex)
	if entries == 0xffff || centralSize == 0xffff || centralOffset == 0xffffffff {
		if directoryEndOffset < 20 {
			return zip.ErrFormat
		}
		locator := make([]byte, 20)
		if _, err := file.ReadAt(locator, directoryEndOffset-20); err != nil {
			return err
		}
		if !bytes.Equal(locator[:4], []byte{'P', 'K', 0x06, 0x07}) ||
			binary.LittleEndian.Uint32(locator[4:8]) != 0 || binary.LittleEndian.Uint32(locator[16:20]) != 1 {
			return zip.ErrFormat
		}
		zip64OffsetRaw := binary.LittleEndian.Uint64(locator[8:16])
		if zip64OffsetRaw > uint64(^uint64(0)>>1) {
			return zip.ErrFormat
		}
		zip64Offset := int64(zip64OffsetRaw)
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
		directoryEndOffset = zip64Offset
	}
	if entries > uint64(maximumEntries) {
		return ErrArchiveEntries
	}
	const maxInt64 = uint64(1<<63 - 1)
	if centralSize > maxInt64 || centralOffset > maxInt64 || centralSize > uint64(directoryEndOffset) {
		return zip.ErrFormat
	}
	effectiveOffset := directoryEndOffset - int64(centralSize)
	if effectiveOffset < 0 || effectiveOffset >= info.Size() {
		return zip.ErrFormat
	}
	if uint64(effectiveOffset) > centralOffset && validZipDirectoryHeaderAt(file, int64(centralOffset), info.Size()) {
		effectiveOffset = int64(centralOffset)
	}
	return scanZipCentralDirectory(file, info.Size(), effectiveOffset, entries, maximumEntries)
}

func scanZipCentralDirectory(file *os.File, fileSize, offset int64, declaredEntries uint64, maximumEntries int) error {
	cursor := offset
	var entries uint64
	header := make([]byte, 46)
	for cursor <= fileSize-int64(len(header)) {
		if _, err := file.ReadAt(header, cursor); err != nil {
			return err
		}
		if !bytes.Equal(header[:4], []byte{'P', 'K', 0x01, 0x02}) {
			break
		}
		entries++
		if entries > uint64(maximumEntries) {
			return ErrArchiveEntries
		}
		variable := int64(binary.LittleEndian.Uint16(header[28:30])) +
			int64(binary.LittleEndian.Uint16(header[30:32])) + int64(binary.LittleEndian.Uint16(header[32:34]))
		step := int64(len(header)) + variable
		if step > fileSize-cursor {
			return zip.ErrFormat
		}
		cursor += step
	}
	if uint16(entries) != uint16(declaredEntries) {
		return zip.ErrFormat
	}
	return nil
}

func validZipDirectoryHeaderAt(file *os.File, offset, fileSize int64) bool {
	const headerSize = 46
	if offset < 0 || offset > fileSize-headerSize {
		return false
	}
	header := make([]byte, headerSize)
	if _, err := file.ReadAt(header, offset); err != nil || !bytes.Equal(header[:4], []byte{'P', 'K', 0x01, 0x02}) {
		return false
	}
	nameLength := int64(binary.LittleEndian.Uint16(header[28:30]))
	extraLength := int64(binary.LittleEndian.Uint16(header[30:32]))
	commentLength := int64(binary.LittleEndian.Uint16(header[32:34]))
	variableLength := nameLength + extraLength + commentLength
	if variableLength > fileSize-offset-headerSize {
		return false
	}
	extra := make([]byte, extraLength)
	if _, err := file.ReadAt(extra, offset+headerSize+nameLength); err != nil {
		return false
	}
	needUncompressed := binary.LittleEndian.Uint32(header[24:28]) == 0xffffffff
	needCompressed := binary.LittleEndian.Uint32(header[20:24]) == 0xffffffff
	needOffset := binary.LittleEndian.Uint32(header[42:46]) == 0xffffffff
	for len(extra) >= 4 {
		tag, size := binary.LittleEndian.Uint16(extra[:2]), int(binary.LittleEndian.Uint16(extra[2:4]))
		extra = extra[4:]
		if size > len(extra) {
			break
		}
		field := extra[:size]
		extra = extra[size:]
		if tag != 0x0001 {
			continue
		}
		for _, needed := range []*bool{&needUncompressed, &needCompressed, &needOffset} {
			if !*needed {
				continue
			}
			if len(field) < 8 {
				return false
			}
			field = field[8:]
			*needed = false
		}
	}
	return !needCompressed && !needOffset
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
	info, statErr := file.Stat()
	if statErr != nil {
		return statErr
	}
	if _, err := fmt.Fprintf(output, "Gzip archive: %s, %d compressed bytes\n", escaped(name), info.Size()); err != nil {
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
		if _, err := fmt.Fprintf(output, "%4d  %10d  %s\n", entries+1, header.Size, escaped(header.Name)); err != nil {
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
