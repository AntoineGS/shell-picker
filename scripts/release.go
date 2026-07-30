package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type target struct{ goos, goarch, suffix string }

var targets = []target{
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "windows", goarch: "amd64", suffix: ".exe"},
	{goos: "windows", goarch: "arm64", suffix: ".exe"},
}

func main() {
	if len(os.Args) < 3 {
		fatal("usage: release.go snapshot|check VERSION [GOOS GOARCH]")
	}
	version := os.Args[2]
	if !strings.HasPrefix(version, "v") || strings.ContainsAny(version, " /\\") {
		fatal("version must be a v-prefixed tag")
	}
	when := releaseTime()
	switch os.Args[1] {
	case "snapshot":
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
		writeChecksums()
	case "check":
		check(version)
	default:
		fatal("unknown operation")
	}
}

func one(version, goos, goarch string, when time.Time) {
	item := findTarget(goos, goarch)
	if err := os.MkdirAll("dist", 0o755); err != nil {
		fatal(err.Error())
	}
	stage, err := os.MkdirTemp("", "shell-picker-release-")
	if err != nil {
		fatal(err.Error())
	}
	defer os.RemoveAll(stage)
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
	archive := filepath.Join("dist", archiveName(version, item))
	if goos == "windows" {
		writeZip(archive, stage, when)
	} else {
		writeTarGz(archive, stage, when)
	}
}

func payloadFiles(binary string) []string {
	files := []string{binary, "README.md", "LICENSE", "adapters/zsh/shell-picker.plugin.zsh", "adapters/nushell/shell-picker.nu"}
	docs, _ := filepath.Glob("docs/*.md")
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
	defer file.Close()
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
	defer file.Close()
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

func writeChecksums() {
	entries, _ := filepath.Glob("dist/shell-picker_*")
	sort.Strings(entries)
	file, err := os.Create("dist/checksums.txt")
	if err != nil {
		fatal(err.Error())
	}
	defer file.Close()
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			fatal(err.Error())
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(file, "%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(path))
	}
}

func check(version string) {
	entries, _ := filepath.Glob("dist/shell-picker_*")
	var archives []string
	for _, path := range entries {
		if strings.HasSuffix(path, ".tar.gz") || strings.HasSuffix(path, ".zip") {
			archives = append(archives, path)
		}
	}
	if len(archives) != len(targets) {
		fatal(fmt.Sprintf("want four archives, got %d", len(archives)))
	}
	verifyChecksums(archives)
	for _, archive := range archives {
		verifyArchive(archive, version)
	}
}

func verifyChecksums(archives []string) {
	data, err := os.ReadFile("dist/checksums.txt")
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

func verifyArchive(path, version string) {
	destination, err := os.MkdirTemp("", "shell-picker-check-")
	if err != nil {
		fatal(err.Error())
	}
	defer os.RemoveAll(destination)
	if strings.HasSuffix(path, ".zip") {
		reader, err := zip.OpenReader(path)
		if err != nil {
			fatal(err.Error())
		}
		for _, entry := range reader.File {
			writeChecked(destination, entry.Name, func() ([]byte, error) {
				handle, err := entry.Open()
				if err != nil {
					return nil, err
				}
				defer handle.Close()
				return io.ReadAll(handle)
			})
		}
		reader.Close()
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
		for {
			header, err := tarReader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				fatal(err.Error())
			}
			data, err := io.ReadAll(tarReader)
			if err != nil {
				fatal(err.Error())
			}
			writeCheckedBytes(destination, header.Name, data)
		}
		gzipReader.Close()
		file.Close()
	}
	want := expectedFiles(filepath.Base(path))
	var got []string
	filepath.Walk(destination, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			got = append(got, filepath.ToSlash(strings.TrimPrefix(path, destination+string(os.PathSeparator))))
		}
		return nil
	})
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		fatal(fmt.Sprintf("unexpected files in %s: %v", path, got))
	}
	for _, name := range got {
		if strings.HasSuffix(name, ".exe") || name == "shell-picker" {
			data, _ := os.ReadFile(filepath.Join(destination, name))
			if strings.Contains(string(data), workspacePath()) {
				fatal("binary contains workspace path")
			}
			if name == "shell-picker" && strings.Contains(filepath.Base(path), "_linux_amd64.") {
				command := exec.Command(filepath.Join(destination, name), "version")
				output, err := command.Output()
				if err != nil || string(output) != "shell-picker "+version+"\n" {
					fatal("injected version check failed")
				}
			}
		}
	}
}

func writeChecked(destination, name string, read func() ([]byte, error)) {
	data, err := read()
	if err != nil {
		fatal(err.Error())
	}
	writeCheckedBytes(destination, name, data)
}
func writeCheckedBytes(destination, name string, data []byte) {
	if filepath.IsAbs(name) || strings.Contains(name, "..") {
		fatal("archive path traversal")
	}
	path := filepath.Join(destination, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatal(err.Error())
	}
	mode := os.FileMode(0o644)
	if isBinary(name) {
		mode = 0o755
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		fatal(err.Error())
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
	docs, _ := filepath.Glob("docs/*.md")
	for _, doc := range docs {
		files = append(files, filepath.ToSlash(doc))
	}
	sort.Strings(files)
	return files
}
func releaseTime() time.Time {
	if value := os.Getenv("SOURCE_DATE_EPOCH"); value != "" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err == nil {
			return time.Unix(seconds, 0).UTC()
		}
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
func workspacePath() string { path, _ := os.Getwd(); return path }
func resetDist() {
	os.RemoveAll("dist")
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
