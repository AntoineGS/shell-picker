//go:build windows

package main

import (
	"io"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var waitNamedPipeW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")

func dialController(address string) (io.ReadWriteCloser, error) {
	wide, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return nil, err
	}
	if err := waitNamedPipe(wide, 30_000); err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(wide, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), address), nil
}

func waitNamedPipe(name *uint16, timeout uint32) error {
	result, _, callErr := waitNamedPipeW.Call(uintptr(unsafe.Pointer(name)), uintptr(timeout))
	runtime.KeepAlive(name)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return windows.ERROR_GEN_FAILURE
}
