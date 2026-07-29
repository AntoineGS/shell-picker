//go:build windows

package callback

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSetCursorWritesWindowsConsoleWithoutStdout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := cursorDevice
	cursorDevice = path
	t.Cleanup(func() { cursorDevice = old })
	SetCursor(protocol.CursorLine)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x1b[5 q" {
		t.Fatalf("cursor bytes=%q", got)
	}
}
