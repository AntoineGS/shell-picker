package fsearchdb

import (
	"context"
	"errors"
	"testing"
)

func TestDecodeRejectsUnsupportedVersion(t *testing.T) {
	_, err := Decode(context.Background(), []byte{'F', 'S', 'D', 'B', 99, 0, 1})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected unsupported format, got %v", err)
	}
}

func TestDefaultPathRejectsRelativeXDG(t *testing.T) {
	got := DefaultPath("/home/test", func(string) string { return "relative" })
	if got != "/home/test/.local/share/fsearch/fsearch.db" {
		t.Fatalf("unexpected database location %q", got)
	}
}
