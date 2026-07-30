package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func verifyArchive(path, version string, when time.Time) {
	streamArchive(path, io.Discard)
	expected := expectedFiles(filepath.Base(path))
	want := make(map[string]bool, len(expected))
	for _, name := range expected {
		want[name] = true
	}
	dataByName := make(map[string][]byte, len(expected))
	lastName := ""
	if strings.HasSuffix(path, ".zip") {
		reader, err := zip.OpenReader(path)
		if err != nil {
			fatal(err.Error())
		}
		defer func() {
			if err := reader.Close(); err != nil {
				fatal(err.Error())
			}
		}()
		for _, entry := range reader.File {
			mode := expectedMode(entry.Name)
			if !validEntryName(entry.Name, want, lastName) || entry.Method != zip.Store || entry.Flags != 8 || entry.ReaderVersion != 20 || entry.CreatorVersion != 0x0314 || entry.ExternalAttrs != 0x80000000|uint32(mode)<<16 || !validZipTimestamp(entry.Extra, when) || entry.Comment != "" || entry.UncompressedSize64 > maxEntrySize || entry.Mode().Perm() != mode || !entry.Modified.Equal(when.UTC()) {
				fatal(fmt.Sprintf("invalid zip metadata for %s", entry.Name))
			}
			handle, err := entry.Open()
			if err != nil {
				fatal(err.Error())
			}
			data, readErr := io.ReadAll(io.LimitReader(handle, maxEntrySize+1))
			closeErr := handle.Close()
			if readErr != nil {
				fatal(readErr.Error())
			}
			if closeErr != nil {
				fatal(closeErr.Error())
			}
			if uint64(len(data)) != entry.UncompressedSize64 || len(data) > maxEntrySize {
				fatal("zip entry exceeds bounded size")
			}
			dataByName[entry.Name] = data
			lastName = entry.Name
		}
	} else {
		file, err := os.Open(path)
		if err != nil {
			fatal(err.Error())
		}
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			fatal(err.Error())
		}
		tarReader := tar.NewReader(gzipReader)
		defer func() {
			if err := gzipReader.Close(); err != nil {
				fatal(err.Error())
			}
			if err := file.Close(); err != nil {
				fatal(err.Error())
			}
		}()
		for {
			header, err := tarReader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				fatal(err.Error())
			}
			if !validEntryName(header.Name, want, lastName) || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxEntrySize || header.Mode != int64(expectedMode(header.Name)) || header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() || len(header.Xattrs) != 0 || !header.ModTime.Equal(when) {
				fatal(fmt.Sprintf("invalid tar metadata for %s", header.Name))
			}
			data, err := io.ReadAll(io.LimitReader(tarReader, maxEntrySize+1))
			if err != nil {
				fatal(err.Error())
			}
			if int64(len(data)) != header.Size || len(data) > maxEntrySize {
				fatal("tar entry exceeds bounded size")
			}
			dataByName[header.Name] = data
			lastName = header.Name
		}
	}
	if len(dataByName) != len(expected) {
		fatal(fmt.Sprintf("unexpected files in %s", path))
	}
	for _, name := range expected {
		data := dataByName[name]
		if isBinary(name) {
			verifyBinary(path, name, data, version)
		}
	}
}

func validEntryName(name string, expected map[string]bool, previous string) bool {
	return expected[name] && name == filepath.ToSlash(name) && !strings.Contains(name, "..") && name != previous && (previous == "" || name > previous)
}

func expectedMode(name string) os.FileMode {
	if isBinary(name) {
		return 0o755
	}
	return 0o644
}

func validZipTimestamp(extra []byte, when time.Time) bool {
	return len(extra) == 9 && extra[0] == 0x55 && extra[1] == 0x54 && extra[2] == 5 && extra[3] == 0 && extra[4] == 1 && binary.LittleEndian.Uint32(extra[5:]) == uint32(when.Unix())
}

func isBinary(name string) bool {
	return name == "shell-picker" || name == "shell-picker.exe"
}

func expectedFiles(archive string) []string {
	item := strings.TrimSuffix(strings.TrimSuffix(archive, ".tar.gz"), ".zip")
	parts := strings.Split(item, "_")
	binary := "shell-picker"
	if parts[len(parts)-1] == "amd64" || parts[len(parts)-1] == "arm64" {
		if strings.Contains(item, "windows") {
			binary += ".exe"
		}
	}
	files := []string{binary, "LICENSE", "README.md", "adapters/nushell/shell-picker.nu", "adapters/zsh/shell-picker.plugin.zsh"}
	docs, err := filepath.Glob("docs/*.md")
	if err != nil {
		fatal(err.Error())
	}
	for _, doc := range docs {
		files = append(files, filepath.ToSlash(doc))
	}
	sort.Strings(files)
	return files
}
