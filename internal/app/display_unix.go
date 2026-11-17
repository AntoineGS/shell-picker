//go:build !windows

package app

import (
	"os"

	"golang.org/x/sys/unix"
)

func pickerHeight(display string, terminal *os.File) string {
	if display == "inline" {
		return "70%"
	}
	if display != "auto" || terminal == nil {
		return ""
	}
	size, err := unix.IoctlGetWinsize(int(terminal.Fd()), unix.TIOCGWINSZ)
	if err == nil && size.Row >= 30 && size.Col >= 80 {
		return "70%"
	}
	return ""
}
