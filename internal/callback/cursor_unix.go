//go:build !windows

package callback

import (
	"os"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var cursorDevice = "/dev/tty"

func SetCursor(shape protocol.CursorShape) {
	sequence := cursorSequence(shape)
	if sequence == "" {
		return
	}
	terminal, err := os.OpenFile(cursorDevice, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	_, _ = terminal.WriteString(sequence)
	_ = terminal.Close()
}

func cursorSequence(shape protocol.CursorShape) string {
	switch shape {
	case protocol.CursorLine:
		return "\x1b[5 q"
	case protocol.CursorBlock:
		return "\x1b[2 q"
	default:
		return ""
	}
}
