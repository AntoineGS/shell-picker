package process

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunnerResolvesExecutableUsingSpecEnvironment(t *testing.T) {
	directory := t.TempDir()
	name := "path-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := helperSpec("exit", "0")
	spec.Path = "path-helper"
	spec.Env = []string{"PATH=" + directory, "GO_WANT_PROCESS_HELPER=1"}
	if err := (Runner{}).Run(context.Background(), spec); err != nil {
		t.Fatalf("Run with spec PATH: %v", err)
	}
}
