//go:build !windows

package callback

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSetCursorWritesUnixTTYWithoutStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tty")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := cursorDevice
	cursorDevice = path
	t.Cleanup(func() { cursorDevice = old })
	SetCursor(protocol.CursorBlock)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x1b[2 q" {
		t.Fatalf("cursor bytes=%q", got)
	}
}
