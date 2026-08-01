package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSpawnFailureFixtureIsRegularFile(t *testing.T) {
	path := newSpawnFailureExecutable(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("spawn-failure fixture mode=%s; want regular file", info.Mode())
	}
	if runtime.GOOS == "windows" && filepath.Ext(path) != ".exe" {
		t.Fatalf("spawn-failure fixture path=%q; want .exe suffix", path)
	}
}

func TestExpectedZoxideOutcomeClassifiesFailureModes(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{mode: "missing", want: "missing"},
		{mode: "spawn-failure", want: "process-error"},
	}
	for _, test := range tests {
		got, ok := expectedZoxideOutcome(test.mode)
		if !ok || got != test.want {
			t.Fatalf("mode=%q outcome=%q ok=%v; want %q", test.mode, got, ok, test.want)
		}
	}
	if _, ok := expectedZoxideOutcome("present"); ok {
		t.Fatal("present mode unexpectedly classified as a failure")
	}
}

func expectedZoxideOutcome(mode string) (string, bool) {
	switch mode {
	case "missing":
		return "missing", true
	case "spawn-failure":
		return "process-error", true
	default:
		return "", false
	}
}

func newSpawnFailureExecutable(t testing.TB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "invalid-executable")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, []byte("not an executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
