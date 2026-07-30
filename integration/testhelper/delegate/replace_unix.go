//go:build !windows

package main

import "syscall"

func replace(path string, arguments, environment []string) error {
	return syscall.Exec(path, arguments, environment)
}
