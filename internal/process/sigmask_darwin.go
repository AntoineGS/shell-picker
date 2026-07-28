//go:build darwin

package process

import (
	"syscall"
	"unsafe"
)

type threadSigset uint32

const (
	threadSigBlock   = 1
	threadSigSetmask = 3
)

var pthreadSigmask = darwinPthreadSigmask

func darwinPthreadSigmask(how int, set, old *threadSigset) error {
	_, _, errno := syscall.Syscall(syscall.SYS___PTHREAD_SIGMASK, uintptr(how),
		uintptr(unsafe.Pointer(set)), uintptr(unsafe.Pointer(old)))
	if errno != 0 {
		return errno
	}
	return nil
}

func sigttouMask() threadSigset                { return 1 << (uint(syscall.SIGTTOU) - 1) }
func platformForegroundRestoreSupported() bool { return true }
