package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func writeChecksumsTo(directory string) {
	entries, err := archivePaths(directory)
	if err != nil {
		fatal(err.Error())
	}
	sort.Strings(entries)
	file, err := os.Create(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if err := file.Close(); err != nil {
			fatal(err.Error())
		}
	}()
	for _, path := range entries {
		sum := archiveDigest(path)
		if _, err := fmt.Fprintf(file, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(path)); err != nil {
			fatal(err.Error())
		}
	}
}

func inferVersion() string {
	return inferVersionIn("dist")
}

func inferVersionIn(directory string) string {
	archives, err := archivePaths(directory)
	if err != nil {
		fatal(err.Error())
	}
	if len(archives) != len(targets) {
		fatal(fmt.Sprintf("want exactly four release archives, got %d", len(archives)))
	}
	version := ""
	seen := make(map[string]bool, len(targets))
	for _, path := range archives {
		candidate, item, ok := parseArchiveName(filepath.Base(path))
		if !ok || seen[item.goos+"/"+item.goarch] {
			fatal(fmt.Sprintf("unexpected archive name %q", filepath.Base(path)))
		}
		seen[item.goos+"/"+item.goarch] = true
		if version == "" {
			version = candidate
		} else if version != candidate {
			fatal("release archives do not share one version")
		}
	}
	validateVersion(version)
	return version
}

func archivePaths(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var archives []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "shell-picker_") {
			if _, _, ok := parseArchiveName(name); !ok {
				return nil, fmt.Errorf("unexpected release file %q", name)
			}
			archives = append(archives, filepath.Join(directory, name))
		}
	}
	sort.Strings(archives)
	return archives, nil
}

var archivePattern = regexp.MustCompile(`^shell-picker_([^_]+)_(linux|windows)_(amd64|arm64)\.(tar\.gz|zip)$`)

func parseArchiveName(name string) (string, target, bool) {
	match := archivePattern.FindStringSubmatch(name)
	if match == nil || (match[2] == "linux") != (match[4] == "tar.gz") {
		return "", target{}, false
	}
	item := findTarget(match[2], match[3])
	return "v" + match[1], item, true
}

func expectedArchivePaths(version, directory string) []string {
	paths := make([]string, 0, len(targets))
	for _, item := range targets {
		paths = append(paths, filepath.Join(directory, archiveName(version, item)))
	}
	sort.Strings(paths)
	return paths
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func checksumArtifacts(directory string) {
	version := inferVersionIn(directory)
	archives := expectedArchivePaths(version, directory)
	if !sameStrings(archives, mustArchivePaths(directory)) {
		fatal("release archive set does not exactly match version and targets")
	}
	checksumPath := filepath.Join(directory, "checksums.txt")
	if _, err := os.Stat(checksumPath); errors.Is(err, os.ErrNotExist) {
		writeChecksumsTo(directory)
	} else if err != nil {
		fatal(err.Error())
	}
	verifyChecksumsAt(directory, archives)
}

func mustArchivePaths(directory string) []string {
	archives, err := archivePaths(directory)
	if err != nil {
		fatal(err.Error())
	}
	return archives
}

func verifyChecksumsAt(directory string, archives []string) {
	checksumPath := filepath.Join(directory, "checksums.txt")
	info, err := os.Stat(checksumPath)
	if err != nil {
		fatal(err.Error())
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 4096 {
		fatal("invalid checksums.txt size or file type")
	}
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		fatal(err.Error())
	}
	var expected bytes.Buffer
	for _, path := range archives {
		sum := archiveDigest(path)
		if _, err := fmt.Fprintf(&expected, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(path)); err != nil {
			fatal(err.Error())
		}
	}
	if !bytes.Equal(data, expected.Bytes()) {
		fatal("checksums.txt does not match archives")
	}
}

func archiveDigest(path string) [32]byte {
	hash := sha256.New()
	streamArchive(path, hash)
	var sum [32]byte
	copy(sum[:], hash.Sum(nil))
	return sum
}

func streamArchive(path string, destination io.Writer) {
	info, err := os.Stat(path)
	if err != nil {
		fatal(err.Error())
	}
	if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxArchiveSize {
		fatal(fmt.Sprintf("archive exceeds %d-byte release limit: %s", maxArchiveSize, path))
	}
	file, err := os.Open(path)
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if err := file.Close(); err != nil {
			fatal(err.Error())
		}
	}()
	count, err := io.CopyN(destination, file, maxArchiveSize+1)
	if err != nil && !errors.Is(err, io.EOF) {
		fatal(err.Error())
	}
	if count > maxArchiveSize {
		fatal(fmt.Sprintf("archive exceeds %d-byte release limit: %s", maxArchiveSize, path))
	}
}

func compareRegularFiles(left, right string) bool {
	leftInfo, err := os.Stat(left)
	if err != nil {
		fatal(err.Error())
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		fatal(err.Error())
	}
	if !leftInfo.Mode().IsRegular() || !rightInfo.Mode().IsRegular() || leftInfo.Size() != rightInfo.Size() || leftInfo.Size() > maxArchiveSize {
		return false
	}
	leftFile, err := os.Open(left)
	if err != nil {
		fatal(err.Error())
	}
	rightFile, err := os.Open(right)
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if err := leftFile.Close(); err != nil {
			fatal(err.Error())
		}
	}()
	defer func() {
		if err := rightFile.Close(); err != nil {
			fatal(err.Error())
		}
	}()
	leftBuffer, rightBuffer := make([]byte, 32*1024), make([]byte, 32*1024)
	for {
		leftCount, leftErr := leftFile.Read(leftBuffer)
		rightCount, rightErr := rightFile.Read(rightBuffer)
		if leftCount != rightCount || !bytes.Equal(leftBuffer[:leftCount], rightBuffer[:rightCount]) {
			return false
		}
		if errors.Is(leftErr, io.EOF) || errors.Is(rightErr, io.EOF) {
			return leftErr == io.EOF && rightErr == io.EOF
		}
		if leftErr != nil {
			fatal(leftErr.Error())
		}
		if rightErr != nil {
			fatal(rightErr.Error())
		}
	}
}
