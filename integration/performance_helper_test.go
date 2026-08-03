package integration

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/process"
)

func TestPerformanceHelperCompletesThroughProcessRunner(t *testing.T) {
	directory := t.TempDir()
	name := "perf-zoxide"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	source := buildPerformanceZoxideHelper(t)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), data, 0o700); err != nil {
		t.Fatal(err)
	}
	environment := replaceEnvironment(os.Environ(), parityHelperEnvironment+"=performance", "GO_PERF_HELPER=fzf", "GO_PERF_ZOXIDE_MODE=empty", "PATH="+directory)
	var stdout bytes.Buffer
	if err := (process.Runner{}).Run(context.Background(), process.Spec{
		Path: "perf-zoxide", Args: []string{"query", "--list"}, Env: environment,
		Stdout: &stdout, Containment: process.ContainmentOwnTree, WaitDelay: time.Second,
	}); err != nil {
		t.Fatalf("performance helper: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("performance helper output=%q", stdout.String())
	}
}
