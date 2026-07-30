//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenPickerTraceRejectsSymlinkWithoutTruncatingTarget(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "trace")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if trace, err := openPickerTrace(link, [16]byte{}, nil); err == nil || trace != nil {
		t.Fatalf("open symlink trace=%v err=%v", trace, err)
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "keep me" {
		t.Fatalf("target=%q err=%v", raw, err)
	}
}

func TestOpenPickerTraceRejectsNonRegularSink(t *testing.T) {
	if trace, err := openPickerTrace("/dev/null", [16]byte{}, nil); err == nil || trace != nil {
		t.Fatalf("open character device trace=%v err=%v", trace, err)
	}
}
