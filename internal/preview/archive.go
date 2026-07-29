package preview

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

func renderZip(ctx context.Context, path string, output io.Writer, limits Limits) error {
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
