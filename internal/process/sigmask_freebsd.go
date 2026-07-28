//go:build freebsd

package process

import (
	"syscall"
	"unsafe"
)

type threadSigset [4]uint32

const (
	threadSigBlock   = 1
	threadSigSetmask = 3
)

var pthreadSigmask = freebsdPthreadSigmask

func freebsdPthreadSigmask(how int, set, old *threadSigset) error {
	_, _, errno := syscall.Syscall(syscall.SYS_SIGPROCMASK, uintptr(how),
		uintptr(unsafe.Pointer(set)), uintptr(unsafe.Pointer(old)))
	if errno != 0 {
		return errno
	}
	return nil
}

func sigttouMask() (mask threadSigset) {
	index := int(syscall.SIGTTOU) - 1
	mask[index/32] = 1 << (index % 32)
	return mask
}
