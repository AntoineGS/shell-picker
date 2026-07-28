//go:build linux

package process

import (
	"reflect"
	"syscall"

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
	index := int(syscall.SIGTTOU) - 1
	words := reflect.ValueOf(&mask).Elem().FieldByName("Val")
	wordBits := int(words.Index(0).Type().Bits())
	words.Index(index / wordBits).SetUint(uint64(1) << uint(index%wordBits))
	return mask
}

func platformForegroundRestoreSupported() bool { return true }
