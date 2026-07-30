package integration

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestChecksumSubcommandGeneratesSortedBasenamesAndRejectsMutations(t *testing.T) {
	archives := checksumFixtureArchives()
	directory := t.TempDir()
	writeChecksumFixture(t, directory, archives)
	runRelease(t, "go", "run", "./scripts/release.go", "checksums", directory)
	data, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	expected := expectedChecksumBytes(archives)
	if !bytes.Equal(data, expected) {
		t.Fatalf("checksum bytes=%q want=%q", data, expected)
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	var names []string
	for _, line := range lines {
		fields := strings.Split(line, "  ")
		if len(fields) != 2 {
			t.Fatalf("malformed checksum line %q", line)
		}
		names = append(names, fields[1])
	}
	if len(lines) != 4 || !sort.StringsAreSorted(names) {
		t.Fatalf("checksums=%q", lines)
	}
	for _, line := range lines {
		fields := strings.Split(line, "  ")
		if len(fields) != 2 || fields[1] != filepath.Base(fields[1]) || strings.ToLower(fields[0]) != fields[0] {
			t.Fatalf("non-basename checksum line %q", line)
		}
	}
	cases := []struct {
		name   string
		mutate func(*testing.T, string, []string)
	}{
		{name: "corrupt", mutate: func(t *testing.T, directory string, archives []string) {
			if err := os.WriteFile(filepath.Join(directory, archives[0]), []byte("corrupt"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "dot checksum path", mutate: func(t *testing.T, directory string, archives []string) {
			mutateChecksumPath(t, directory, "./"+archives[0])
		}},
		{name: "dist checksum path", mutate: func(t *testing.T, directory string, archives []string) {
			mutateChecksumPath(t, directory, "dist/"+archives[0])
		}},
		{name: "missing archive", mutate: func(t *testing.T, directory string, archives []string) {
			if err := os.Remove(filepath.Join(directory, archives[0])); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "extra archive", mutate: func(t *testing.T, directory string, archives []string) {
			if err := os.WriteFile(filepath.Join(directory, "shell-picker_1.2.3_linux_extra.tar.gz"), []byte("extra"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "renamed archive", mutate: func(t *testing.T, directory string, archives []string) {
			if err := os.Rename(filepath.Join(directory, archives[0]), filepath.Join(directory, "renamed.tar.gz")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeChecksumFixture(t, directory, archives)
			runRelease(t, "go", "run", "./scripts/release.go", "checksums", directory)
			test.mutate(t, directory, archives)
			if runReleaseFailureInDirectory(t, directory, "go", "run", "./scripts/release.go", "checksums", directory) == nil {
				t.Fatalf("checksums subcommand accepted %s", test.name)
			}
		})
	}
}

func TestChecksumSubcommandRejectsNonCanonicalChecksumBytes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "leading LF", mutate: func(data []byte) []byte { return append([]byte{'\n'}, data...) }},
		{name: "leading space", mutate: func(data []byte) []byte { return append([]byte{' '}, data...) }},
		{name: "trailing blank", mutate: func(data []byte) []byte { return append(data, '\n') }},
		{name: "trailing space", mutate: func(data []byte) []byte { return append(append([]byte{}, data[:len(data)-1]...), []byte(" \n")...) }},
		{name: "CRLF", mutate: func(data []byte) []byte { return bytes.ReplaceAll(data, []byte{'\n'}, []byte{'\r', '\n'}) }},
		{name: "missing final LF", mutate: func(data []byte) []byte { return data[:len(data)-1] }},
		{name: "extra LF", mutate: func(data []byte) []byte { return append(data, '\n') }},
		{name: "uppercase", mutate: func(data []byte) []byte {
			for index, value := range data {
				if value >= 'a' && value <= 'f' {
					data[index] = value - ('a' - 'A')
					break
				}
			}
			return data
		}},
		{name: "single separator", mutate: func(data []byte) []byte { return bytes.Replace(data, []byte("  "), []byte(" "), 1) }},
		{name: "order", mutate: func(data []byte) []byte {
			lines := bytes.Split(data, []byte{'\n'})
			lines[0], lines[1] = lines[1], lines[0]
			return bytes.Join(lines, []byte{'\n'})
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			archives := checksumFixtureArchives()
			writeChecksumFixture(t, directory, archives)
			runRelease(t, "go", "run", "./scripts/release.go", "checksums", directory)
			data, err := os.ReadFile(filepath.Join(directory, "checksums.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "checksums.txt"), test.mutate(data), 0o644); err != nil {
				t.Fatal(err)
			}
			if runReleaseFailureInDirectory(t, directory, "go", "run", "./scripts/release.go", "checksums", directory) == nil {
				t.Fatalf("accepted %s checksum mutation", test.name)
			}
		})
	}
}

func TestChecksumSubcommandRejectsSparseOversizedArchive(t *testing.T) {
	directory := t.TempDir()
	archives := checksumFixtureArchives()
	writeChecksumFixture(t, directory, archives)
	path := filepath.Join(directory, archives[0])
	if err := os.Truncate(path, 128*1024*1024+1); err != nil {
		t.Fatal(err)
	}
	if runReleaseFailureInDirectory(t, directory, "go", "run", "./scripts/release.go", "checksums", directory) == nil {
		t.Fatal("accepted archive over the explicit release size limit")
	}
}

func checksumFixtureArchives() []string {
	return []string{"shell-picker_1.2.3_linux_amd64.tar.gz", "shell-picker_1.2.3_linux_arm64.tar.gz", "shell-picker_1.2.3_windows_amd64.zip", "shell-picker_1.2.3_windows_arm64.zip"}
}

func writeChecksumFixture(t *testing.T, directory string, archives []string) {
	t.Helper()
	for _, name := range archives {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func expectedChecksumBytes(archives []string) []byte {
	var output bytes.Buffer
	sort.Strings(archives)
	for _, name := range archives {
		sum := sha256.Sum256([]byte(name))
		fmt.Fprintf(&output, "%x  %s\n", sum, name)
	}
	return output.Bytes()
}

func mutateChecksumPath(t *testing.T, directory, replacement string) {
	t.Helper()
	path := filepath.Join(directory, "checksums.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := "  " + checksumFixtureArchives()[0]
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, "  "+replacement, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runReleaseFailureInDirectory(t *testing.T, directory, name string, args ...string) error {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Logf("expected %s failure: %v\n%s", strings.Join(args, " "), err, output)
	}
	return err
}
