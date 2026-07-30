package release

import (
	"fmt"
	"os"
	"path/filepath"
)

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
		if !compareRegularFiles(original, path) {
			fatal(fmt.Sprintf("rebuild differs for %s", originalName))
		}
	}
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
