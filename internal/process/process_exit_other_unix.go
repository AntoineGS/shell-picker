//go:build !windows && !linux && !freebsd

package process

import "syscall"

func nonReapingExitSupported() bool { return false }
func observeProcessExit(int) error  { return syscall.ENOTSUP }
