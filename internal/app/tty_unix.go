//go:build !windows

package app

import "os"

func pickerTerminal(injected *os.File) (*os.File, bool, error) {
	if injected != nil {
		return injected, false, nil
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	return terminal, err == nil, err
}
