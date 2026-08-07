package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
	files := []string{binary, "README.md", "LICENSE", "adapters/zsh/shell-picker.plugin.zsh", "adapters/nushell/shell-picker.nu", "adapters/powershell/shell-picker.psd1", "adapters/powershell/shell-picker.psm1", "adapters/powershell/shell-picker-core.ps1"}
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
		left := filepath.ToSlash(strings.TrimPrefix(names[i], stage+string(os.PathSeparator)))
		right := filepath.ToSlash(strings.TrimPrefix(names[j], stage+string(os.PathSeparator)))
		return left < right
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
