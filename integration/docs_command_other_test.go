//go:build !linux

package integration

import "os/exec"

func runPickerDocCommand(command *exec.Cmd) ([]byte, error) {
	return command.CombinedOutput()
}
