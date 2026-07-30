//go:build windows

package main

import (
	"os"
	"os/exec"
)

func replace(path string, arguments, environment []string) error {
	command := exec.Command(path, arguments[1:]...)
	command.Env = environment
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
