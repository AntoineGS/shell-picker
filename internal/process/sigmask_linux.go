//go:build linux

package process

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type threadSigset = unix.Sigset_t

const (
	threadSigBlock   = unix.SIG_BLOCK
	threadSigSetmask = unix.SIG_SETMASK
)

var pthreadSigmask = unix.PthreadSigmask

func sigttouMask() threadSigset {
	var mask threadSigset
	bytes := unsafe.Slice((*byte)(unsafe.Pointer(&mask)), int(unsafe.Sizeof(mask)))
	index := int(syscall.SIGTTOU) - 1
	bytes[index/8] |= byte(1 << (index % 8))
	return mask
}
