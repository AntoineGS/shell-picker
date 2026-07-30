package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxEntrySize = 32 << 20

type target struct{ goos, goarch, suffix string }

var targets = []target{
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "windows", goarch: "amd64", suffix: ".exe"},
	{goos: "windows", goarch: "arm64", suffix: ".exe"},
}

func main() {
	if len(os.Args) < 2 {
		fatal("usage: release.go snapshot VERSION [GOOS GOARCH] | check [VERSION]")
	}
	version := ""
	switch os.Args[1] {
	case "snapshot":
		if len(os.Args) < 3 {
			fatal("snapshot requires VERSION")
		}
		version = os.Args[2]
		validateVersion(version)
		when := releaseTime()
		if len(os.Args) == 5 {
			one(version, os.Args[3], os.Args[4], when)
			return
		}
		if len(os.Args) != 3 {
			fatal("snapshot accepts either no target or GOOS GOARCH")
		}
		resetDist()
		for _, item := range targets {
			one(version, item.goos, item.goarch, when)
		}
		writeChecksumsTo("dist")
	case "check":
		if len(os.Args) > 3 {
			fatal("check accepts at most VERSION")
		}
		if len(os.Args) == 3 {
			version = os.Args[2]
			validateVersion(version)
		}
		if version == "" {
			version = inferVersion()
		}
		check(version)
	case "checksums":
		if len(os.Args) != 3 {
			fatal("checksums requires DIRECTORY")
		}
		checksumArtifacts(os.Args[2])
	default:
		fatal("unknown operation")
	}
}

func validateVersion(version string) {
	if !strings.HasPrefix(version, "v") || strings.ContainsAny(version, " /\\") {
		fatal("version must be a v-prefixed tag")
	}
}

func one(version, goos, goarch string, when time.Time) {
	oneIn(version, "dist", goos, goarch, when)
}

func oneIn(version, output, goos, goarch string, when time.Time) {
	item := findTarget(goos, goarch)
	if err := os.MkdirAll(output, 0o755); err != nil {
		fatal(err.Error())
	}
	stage, err := os.MkdirTemp("", "shell-picker-release-")
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			fatal(err.Error())
		}
	}()
	binary := filepath.Join(stage, "shell-picker"+item.suffix)
	args := []string{"build", "-trimpath", "-buildvcs=true", "-ldflags", "-s -w -X main.version=" + version, "-o", binary, "./cmd/shell-picker"}
	command := exec.Command("go", args...)
	command.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		fatal(fmt.Sprintf("go build %s/%s: %v\n%s", goos, goarch, err, output))
	}
	for _, source := range payloadFiles(binary) {
		copyPayload(stage, source)
	}
	archive := filepath.Join(output, archiveName(version, item))
	if goos == "windows" {
		writeZip(archive, stage, when)
	} else {
		writeTarGz(archive, stage, when)
	}
}

func payloadFiles(binary string) []string {
	files := []string{binary, "README.md", "LICENSE", "adapters/zsh/shell-picker.plugin.zsh", "adapters/nushell/shell-picker.nu"}
	docs, err := filepath.Glob("docs/*.md")
	if err != nil {
		fatal(err.Error())
	}
	return append(files, docs...)
}

func copyPayload(stage, source string) {
	name := filepath.ToSlash(source)
	if strings.HasPrefix(name, stage+"/") || filepath.IsAbs(source) {
		name = filepath.Base(source)
	}
	destination := filepath.Join(stage, name)
	if destination == source {
		return
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		fatal(err.Error())
	}
	data, err := os.ReadFile(source)
	if err != nil {
		fatal(err.Error())
	}
	if err := os.WriteFile(destination, data, 0o644); err != nil {
		fatal(err.Error())
	}
}

func writeTarGz(path, stage string, when time.Time) {
	file, err := os.Create(path)
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if err := file.Close(); err != nil {
			fatal(err.Error())
		}
	}()
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	writeEntries(stage, func(name string, data []byte) {
		mode := int64(0o644)
		if isBinary(name) {
			mode = 0o755
		}
		header := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: when, Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatPAX}
		if err := tarWriter.WriteHeader(header); err != nil {
			fatal(err.Error())
		}
		if _, err := tarWriter.Write(data); err != nil {
			fatal(err.Error())
		}
	})
	if err := tarWriter.Close(); err != nil {
		fatal(err.Error())
	}
	if err := gzipWriter.Close(); err != nil {
		fatal(err.Error())
	}
}

func writeZip(path, stage string, when time.Time) {
	file, err := os.Create(path)
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if err := file.Close(); err != nil {
			fatal(err.Error())
		}
	}()
	writer := zip.NewWriter(file)
	writeEntries(stage, func(name string, data []byte) {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetModTime(when.UTC())
		if isBinary(name) {
			header.SetMode(0o755)
		} else {
			header.SetMode(0o644)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			fatal(err.Error())
		}
		if _, err := entry.Write(data); err != nil {
			fatal(err.Error())
		}
	})
	if err := writer.Close(); err != nil {
		fatal(err.Error())
	}
}

func writeEntries(stage string, write func(string, []byte)) {
	var names []string
	err := filepath.Walk(stage, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			names = append(names, path)
		}
		return nil
	})
	if err != nil {
		fatal(err.Error())
	}
	sort.Slice(names, func(i, j int) bool {
		return filepath.ToSlash(strings.TrimPrefix(names[i], stage+string(os.PathSeparator))) < filepath.ToSlash(strings.TrimPrefix(names[j], stage+string(os.PathSeparator)))
	})
	for _, path := range names {
		name := filepath.ToSlash(strings.TrimPrefix(path, stage+string(os.PathSeparator)))
		data, err := os.ReadFile(path)
		if err != nil {
			fatal(err.Error())
		}
		write(name, data)
	}
}

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
		data, err := os.ReadFile(path)
		if err != nil {
			fatal(err.Error())
		}
		sum := sha256.Sum256(data)
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

func check(version string) {
	archives, err := archivePaths("dist")
	if err != nil {
		fatal(err.Error())
	}
	expected := expectedArchivePaths(version, "dist")
	validateDistDirectory(expected)
	if !sameStrings(archives, expected) {
		fatal("release archive set does not exactly match version and targets")
	}
	when := releaseTime()
	for _, archive := range archives {
		verifyArchive(archive, version, when)
	}
	verifyChecksumsAt("dist", archives)
	temporary, err := os.MkdirTemp("", "shell-picker-release-rebuild-")
	if err != nil {
		fatal(err.Error())
	}
	defer func() {
		if err := os.RemoveAll(temporary); err != nil {
			fatal(err.Error())
		}
	}()
	for _, item := range targets {
		oneIn(version, temporary, item.goos, item.goarch, when)
	}
	writeChecksumsTo(temporary)
	for _, path := range append(expectedArchivePaths(version, temporary), filepath.Join(temporary, "checksums.txt")) {
		originalName := filepath.Base(path)
		original := filepath.Join("dist", originalName)
		want, err := os.ReadFile(original)
		if err != nil {
			fatal(err.Error())
		}
		got, err := os.ReadFile(path)
		if err != nil {
			fatal(err.Error())
		}
		if !bytes.Equal(want, got) {
			fatal(fmt.Sprintf("rebuild differs for %s", originalName))
		}
	}
}

func expectedArchivePaths(version, directory string) []string {
	paths := make([]string, 0, len(targets))
	for _, item := range targets {
		paths = append(paths, filepath.Join(directory, archiveName(version, item)))
	}
	sort.Strings(paths)
	return paths
}

func validateDistDirectory(expected []string) {
	entries, err := os.ReadDir("dist")
	if err != nil {
		fatal(err.Error())
	}
	expectedNames := make(map[string]bool, len(expected)+1)
	for _, path := range expected {
		expectedNames[filepath.Base(path)] = true
	}
	expectedNames["checksums.txt"] = true
	for _, entry := range entries {
		if !expectedNames[entry.Name()] {
			fatal(fmt.Sprintf("unexpected dist entry %q", entry.Name()))
		}
	}
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
	data, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		fatal(err.Error())
	}
	want := strings.TrimSpace(string(data))
	gotByName := make(map[string]string, len(archives))
	for _, path := range archives {
		contents, err := os.ReadFile(path)
		if err != nil {
			fatal(err.Error())
		}
		sum := sha256.Sum256(contents)
		gotByName[filepath.Base(path)] = fmt.Sprintf("%s  %s", hex.EncodeToString(sum[:]), filepath.Base(path))
	}
	var got []string
	for _, path := range archives {
		got = append(got, gotByName[filepath.Base(path)])
	}
	sort.Slice(got, func(i, j int) bool {
		return strings.TrimPrefix(got[i], strings.SplitN(got[i], "  ", 2)[0]+"  ") < strings.TrimPrefix(got[j], strings.SplitN(got[j], "  ", 2)[0]+"  ")
	})
	if strings.Join(got, "\n") != want {
		fatal("checksums.txt does not match archives")
	}
}

func verifyArchive(path, version string, when time.Time) {
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

func verifyBinary(archive, name string, data []byte, version string) {
	workspace, err := os.Getwd()
	if err != nil {
		fatal(err.Error())
	}
	if bytes.Contains(data, []byte(workspace)) || !bytes.Contains(data, []byte(version)) || bytes.Contains(data, []byte("shell-picker dev")) {
		fatal(fmt.Sprintf("invalid version or workspace path in %s", archive))
	}
	base := filepath.Base(archive)
	if strings.Contains(base, "_linux_") {
		file, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			fatal(err.Error())
		}
		want := elf.EM_X86_64
		if strings.Contains(base, "_arm64.") {
			want = elf.EM_AARCH64
		}
		if file.Machine != want {
			fatal("wrong ELF architecture")
		}
	} else {
		file, err := pe.NewFile(bytes.NewReader(data))
		if err != nil {
			fatal(err.Error())
		}
		defer func() {
			if err := file.Close(); err != nil {
				fatal(err.Error())
			}
		}()
		want := uint16(pe.IMAGE_FILE_MACHINE_AMD64)
		if strings.Contains(base, "_arm64.") {
			want = pe.IMAGE_FILE_MACHINE_ARM64
		}
		if file.FileHeader.Machine != want {
			fatal("wrong PE architecture")
		}
	}
	if name == "shell-picker" && strings.Contains(base, "_linux_amd64.") {
		temporary, err := os.CreateTemp("", "shell-picker-version-")
		if err != nil {
			fatal(err.Error())
		}
		path := temporary.Name()
		defer func() {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				fatal(err.Error())
			}
		}()
		if _, err := temporary.Write(data); err != nil {
			fatal(err.Error())
		}
		if err := temporary.Chmod(0o755); err != nil {
			fatal(err.Error())
		}
		if err := temporary.Close(); err != nil {
			fatal(err.Error())
		}
		output, err := exec.Command(path, "version").Output()
		if err != nil || string(output) != "shell-picker "+version+"\n" {
			fatal("injected version execution failed")
		}
	}
}

func isBinary(name string) bool { return name == "shell-picker" || name == "shell-picker.exe" }
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
func releaseTime() time.Time {
	if value := os.Getenv("SOURCE_DATE_EPOCH"); value != "" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			fatal(fmt.Sprintf("invalid SOURCE_DATE_EPOCH: %v", err))
		}
		return time.Unix(seconds, 0).UTC()
	}
	command := exec.Command("git", "show", "-s", "--format=%ct", "HEAD")
	output, err := command.Output()
	if err != nil {
		fatal(err.Error())
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		fatal(err.Error())
	}
	return time.Unix(seconds, 0).UTC()
}
func resetDist() {
	if err := os.RemoveAll("dist"); err != nil {
		fatal(err.Error())
	}
	if err := os.MkdirAll("dist", 0o755); err != nil {
		fatal(err.Error())
	}
}
func findTarget(goos, goarch string) target {
	for _, item := range targets {
		if item.goos == goos && item.goarch == goarch {
			return item
		}
	}
	fatal("unsupported target")
	return target{}
}
func archiveName(version string, item target) string {
	return fmt.Sprintf("shell-picker_%s_%s_%s.%s", strings.TrimPrefix(version, "v"), item.goos, item.goarch, map[bool]string{true: "zip", false: "tar.gz"}[item.goos == "windows"])
}
func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
