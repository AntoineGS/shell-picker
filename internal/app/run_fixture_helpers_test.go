//go:build !windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func cancellationZoxideFixture(t *testing.T, path string) string {
	t.Helper()
	directory := t.TempDir()
	executable := filepath.Join(directory, "zoxide")
	script := fmt.Sprintf("#!/bin/sh\nsleep 10\nprintf '%%s\\n' '%s'\n", path)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable
}
