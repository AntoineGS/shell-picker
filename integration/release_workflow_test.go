package integration

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseWorkflowContract(t *testing.T) {
	text := readWorkflow(t, "release.yml")
	requireAll(t, text, "tags:", "- 'v*'", "actions/checkout@v5", "actions/setup-go@v6", "actions/upload-artifact@v4", "actions/download-artifact@v4", "linux", "windows", "amd64", "arm64", "checksums.txt", "gh release create")
	rejectAll(t, text, "latest", "continue-on-error: true", "--force")
	if strings.Contains(text, "sha256sum") {
		t.Fatal("workflow must delegate checksum handling to the tested release tool")
	}
	requireAll(t, text, "permissions:\n  contents: write", "test \"${GITHUB_REF_TYPE}\" = tag", "--verify-tag", "--generate-notes", "--title \"$GITHUB_REF_NAME\"", "go run ./scripts/release.go check \"$GITHUB_REF_NAME\"")
	releaseStart := strings.Index(text, "gh release create ")
	if releaseStart < 0 || strings.Count(text[releaseStart:], "checksums.txt") != 1 {
		t.Fatal("release publication must include checksums exactly once")
	}
	if strings.Contains(text, "workflow_dispatch:") || strings.Contains(text, "pull_request:") || strings.Contains(text, "branches:") {
		t.Fatal("release workflow is not tag-only")
	}
	if strings.Count(text, "archive: shell-picker-") != 4 || strings.Count(text, "goos: linux") != 2 || strings.Count(text, "goos: windows") != 2 {
		t.Fatal("release matrix is not exactly four platform entries")
	}
	requireAll(t, text, "go run ./scripts/release.go checksums dist")
}

func TestReleaseCheckDoesNotInterpolateVersion(t *testing.T) {
	command := exec.Command("make", "-n", "release-check", "VERSION=v1;id")
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "v1;id") || strings.Contains(string(output), " id") {
		t.Fatalf("release-check interpolated VERSION into command: %s", output)
	}
}

func TestReleaseCheckInfersTheOnlyArchiveVersion(t *testing.T) {
	runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
	runRelease(t, "make", "release-check")
}

func TestReleaseCheckRejectsChecksumPathPrefixes(t *testing.T) {
	runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
	path := filepath.Join("..", "dist", "checksums.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "  shell-picker_", "  dist/shell-picker_")), 0o644); err != nil {
		t.Fatal(err)
	}
	if runReleaseFailure(t, "make", "release-check") == nil {
		t.Fatal("release-check accepted a checksum path prefix")
	}
}

func TestReleaseCheckRejectsNonCanonicalArchiveSet(t *testing.T) {
	runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
	old := filepath.Join("..", "dist", "shell-picker_0.0.0-test_linux_amd64.tar.gz")
	bad := filepath.Join("..", "dist", "shell-picker_0.0.0-test_linux_ppc64.tar.gz")
	if err := os.Rename(old, bad); err != nil {
		t.Fatal(err)
	}
	if runReleaseFailure(t, "make", "release-check") == nil {
		t.Fatal("release-check accepted a noncanonical archive set")
	}
}

func TestReleaseCheckRejectsMissingArchive(t *testing.T) {
	runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
	path := filepath.Join("..", "dist", "shell-picker_0.0.0-test_windows_arm64.zip")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if runReleaseFailure(t, "make", "release-check") == nil {
		t.Fatal("release-check accepted a missing archive")
	}
}

func TestReleaseCheckRejectsWrongBinaryArchitecture(t *testing.T) {
	runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
	amd64 := filepath.Join("..", "dist", "shell-picker_0.0.0-test_linux_amd64.tar.gz")
	arm64 := filepath.Join("..", "dist", "shell-picker_0.0.0-test_linux_arm64.tar.gz")
	data, err := os.ReadFile(amd64)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arm64, data, 0o644); err != nil {
		t.Fatal(err)
	}
	updateChecksum(t, filepath.Base(arm64), data)
	if runReleaseFailure(t, "make", "release-check") == nil {
		t.Fatal("release-check accepted an amd64 binary under the arm64 name")
	}
}

func TestReleaseCheckRejectsTarMetadataAndOrderingMutations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]tarMutation)
	}{
		{name: "duplicate", mutate: func(entries []tarMutation) { entries[len(entries)-1] = entries[0] }},
		{name: "mode", mutate: func(entries []tarMutation) { entries[0].Header.Mode = 0o600 }},
		{name: "timestamp", mutate: func(entries []tarMutation) { entries[0].Header.ModTime = entries[0].Header.ModTime.Add(time.Second) }},
		{name: "owner", mutate: func(entries []tarMutation) { entries[0].Header.Uid = 7 }},
		{name: "order", mutate: func(entries []tarMutation) { entries[0], entries[1] = entries[1], entries[0] }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
			path := filepath.Join("..", "dist", "shell-picker_0.0.0-test_linux_amd64.tar.gz")
			mutateTar(t, path, test.mutate)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			updateChecksum(t, filepath.Base(path), data)
			if runReleaseFailure(t, "make", "release-check") == nil {
				t.Fatalf("release-check accepted %s mutation", test.name)
			}
		})
	}
}

func TestReleaseCheckRejectsWrongVersionAndWorkspaceLeak(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]tarMutation)
	}{
		{name: "wrong version", mutate: func(entries []tarMutation) {
			for index := range entries {
				if entries[index].Header.Name == "shell-picker" {
					entries[index].Data = bytes.Replace(entries[index].Data, []byte("v0.0.0-test"), []byte("v9.9.9-test"), 1)
					entries[index].Header.Size = int64(len(entries[index].Data))
				}
			}
		}},
		{name: "workspace leak", mutate: func(entries []tarMutation) {
			workspace, err := filepath.Abs("..")
			if err != nil {
				panic(err)
			}
			for index := range entries {
				if entries[index].Header.Name == "shell-picker" {
					entries[index].Data = append(entries[index].Data, []byte(workspace)...)
					entries[index].Header.Size = int64(len(entries[index].Data))
				}
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
			path := filepath.Join("..", "dist", "shell-picker_0.0.0-test_linux_amd64.tar.gz")
			mutateTar(t, path, test.mutate)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			updateChecksum(t, filepath.Base(path), data)
			if runReleaseFailure(t, "make", "release-check") == nil {
				t.Fatalf("release-check accepted %s mutation", test.name)
			}
		})
	}
}

func TestReleaseCheckRejectsPayloadDriftAfterChecksumUpdate(t *testing.T) {
	runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
	path := filepath.Join("..", "dist", "shell-picker_0.0.0-test_linux_amd64.tar.gz")
	mutateTar(t, path, func(entries []tarMutation) {
		for index := range entries {
			if entries[index].Header.Name == "README.md" {
				entries[index].Data[0] ^= 1
			}
		}
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updateChecksum(t, filepath.Base(path), data)
	if runReleaseFailure(t, "make", "release-check") == nil {
		t.Fatal("release-check accepted payload drift after checksum update")
	}
}

func TestReleaseCheckRejectsZipMetadataMutation(t *testing.T) {
	runRelease(t, "make", "release-snapshot", "VERSION=v0.0.0-test")
	path := filepath.Join("..", "dist", "shell-picker_0.0.0-test_windows_amd64.zip")
	rewriteZip(t, path, func(entries []zipMutation) { entries[0].Header.SetMode(0o600) })
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updateChecksum(t, filepath.Base(path), data)
	if runReleaseFailure(t, "make", "release-check") == nil {
		t.Fatal("release-check accepted a ZIP metadata mutation")
	}
}

func TestInjectedVersion(t *testing.T) {
	binary := buildReleaseCommand(t, `-X main.version=v1.2.3`)
	out := runCommand(t, binary, "version")
	if out != "shell-picker v1.2.3\n" {
		t.Fatalf("out=%q", out)
	}
}

func buildReleaseCommand(t *testing.T, ldflags string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "shell-picker")
	command := exec.Command("go", "build", "-ldflags", ldflags, "-o", binary, "../cmd/shell-picker")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

func runCommand(t *testing.T, binary string, args ...string) string {
	t.Helper()
	output, err := exec.Command(binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runRelease(t *testing.T, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = ".."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, output)
	}
}

func runReleaseFailure(t *testing.T, name string, args ...string) error {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Logf("expected %s %s failure: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return err
}

func updateChecksum(t *testing.T, name string, data []byte) {
	t.Helper()
	path := filepath.Join("..", "dist", "checksums.txt")
	checksums, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	lines := strings.Split(strings.TrimSuffix(string(checksums), "\n"), "\n")
	for index, line := range lines {
		if strings.HasSuffix(line, "  "+name) {
			lines[index] = hex.EncodeToString(sum[:]) + "  " + name
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

type tarMutation struct {
	Header tar.Header
	Data   []byte
}

type zipMutation struct {
	Header zip.FileHeader
	Data   []byte
}

func mutateTar(t *testing.T, path string, mutate func([]tarMutation)) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	tarReader := tar.NewReader(reader)
	var entries []tarMutation
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, tarMutation{Header: *header, Data: data})
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	mutate(entries)
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		entry.Header.Size = int64(len(entry.Data))
		if err := tarWriter.WriteHeader(&entry.Header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, output.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rewriteZip(t *testing.T, path string, mutate func([]zipMutation)) {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []zipMutation
	for _, entry := range reader.File {
		handle, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(handle)
		if err != nil {
			t.Fatal(err)
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, zipMutation{Header: entry.FileHeader, Data: data})
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	mutate(entries)
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		entry.Header.UncompressedSize64 = 0
		entry.Header.CompressedSize64 = 0
		entry.Header.CRC32 = 0
		handle, err := writer.CreateHeader(&entry.Header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handle.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, output.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
