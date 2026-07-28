//go:build !windows && !linux && !darwin && !freebsd

package process

import "syscall"

type threadSigset uintptr

const (
	threadSigBlock   = 1
	threadSigSetmask = 3
)

var pthreadSigmask = func(int, *threadSigset, *threadSigset) error { return syscall.ENOTSUP }

func sigttouMask() threadSigset { return 1 << (uint(syscall.SIGTTOU) - 1) }
