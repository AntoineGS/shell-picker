//go:build windows

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	windowsTraceRecordLockWaitSliceMS = 100
	windowsTraceRecordLockDeadlineMS  = 2000
)

type windowsTraceSink struct {
	mu       sync.Mutex
	file     *os.File
	flush    func(*os.File) error
	close    func(*os.File) error
	write    func(*os.File, []byte) (int, error)
	lock     *windowsTraceRecordLock
	disk     bool
	closed   bool
	closeErr error
}

type windowsTraceRecordLock struct {
	handle  windows.Handle
	created bool
	wait    func(windows.Handle, uint32) (uint32, error)
	release func(windows.Handle) error
	close   func(windows.Handle) error
}

func (sink *windowsTraceSink) Write(data []byte) (int, error) {
	var written int
	err := sink.withRecordLock(func(file *os.File) error {
		var err error
		written, err = sink.writeLocked(file, data)
		return err
	})
	return written, err
}

func (sink *windowsTraceSink) WriteRecord(data []byte) error {
	return sink.withRecordLock(func(file *os.File) error {
		written, err := sink.writeLocked(file, data)
		if err != nil {
			return err
		}
		if written != len(data) {
			return io.ErrShortWrite
		}
		return nil
	})
}

func (sink *windowsTraceSink) withRecordLock(write func(*os.File) error) (err error) {
	if sink == nil {
		return os.ErrClosed
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed || sink.file == nil {
		return os.ErrClosed
	}
	if sink.lock == nil {
		return errors.New("trace record lock unavailable")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	status, waitErr := sink.lock.waitForOwner()
	if waitErr != nil {
		return waitErr
	}
	if status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED {
		return fmt.Errorf("wait for trace record lock: unexpected wait status 0x%x", status)
	}
	defer func() {
		err = errors.Join(err, sink.lock.releaseOwner())
	}()
	return write(sink.file)
}

func (sink *windowsTraceSink) writeLocked(file *os.File, data []byte) (int, error) {
	if sink.disk {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			return 0, err
		}
	}
	write := sink.write
	if write == nil {
		write = (*os.File).Write
	}
	written := 0
	for written < len(data) {
		count, err := write(file, data[written:])
		if count < 0 || count > len(data)-written {
			return written, fmt.Errorf("trace write: invalid byte count %d", count)
		}
		written += count
		if err != nil {
			if errors.Is(err, io.ErrShortWrite) && count > 0 && written < len(data) {
				continue
			}
			return written, err
		}
		if count == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (sink *windowsTraceSink) Close() error {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.closed {
		return sink.closeErr
	}
	sink.closed = true
	file := sink.file
	sink.file = nil
	flush := sink.flush
	if flush == nil {
		flush = func(file *os.File) error {
			return windows.FlushFileBuffers(windows.Handle(file.Fd()))
		}
	}
	closeFile := sink.close
	if closeFile == nil {
		closeFile = func(file *os.File) error { return file.Close() }
	}
	var fileErr error
	if file != nil {
		fileErr = errors.Join(flush(file), closeFile(file))
	}
	var lockErr error
	if sink.lock != nil {
		lockErr = sink.lock.closeHandle()
		sink.lock = nil
	}
	sink.closeErr = errors.Join(fileErr, lockErr)
	return sink.closeErr
}

func (lock *windowsTraceRecordLock) waitForOwner() (uint32, error) {
	if lock == nil || lock.handle == 0 {
		return windows.WAIT_FAILED, errors.New("trace record lock is closed")
	}
	wait := lock.wait
	if wait == nil {
		wait = windows.WaitForSingleObject
	}
	for elapsed := uint32(0); elapsed < windowsTraceRecordLockDeadlineMS; elapsed += windowsTraceRecordLockWaitSliceMS {
		waitMilliseconds := uint32(windowsTraceRecordLockWaitSliceMS)
		if remaining := uint32(windowsTraceRecordLockDeadlineMS) - elapsed; remaining < waitMilliseconds {
			waitMilliseconds = remaining
		}
		status, err := wait(lock.handle, waitMilliseconds)
		if err != nil {
			return status, errors.New("wait for trace record lock")
		}
		if status != uint32(windows.WAIT_TIMEOUT) {
			return status, nil
		}
	}
	return uint32(windows.WAIT_TIMEOUT), nil
}

func (lock *windowsTraceRecordLock) releaseOwner() error {
	if lock == nil || lock.handle == 0 {
		return errors.New("release trace record lock")
	}
	release := lock.release
	if release == nil {
		release = windows.ReleaseMutex
	}
	if err := release(lock.handle); err != nil {
		return errors.New("release trace record lock")
	}
	return nil
}

func (lock *windowsTraceRecordLock) closeHandle() error {
	if lock == nil || lock.handle == 0 {
		return nil
	}
	handle := lock.handle
	lock.handle = 0
	closeHandle := lock.close
	if closeHandle == nil {
		closeHandle = windows.CloseHandle
	}
	if err := closeHandle(handle); err != nil {
		return errors.New("close trace record lock")
	}
	return nil
}

func canonicalTraceSinkIdentity(path string) string {
	if isCanonicalNamedPipePath(path) {
		return strings.ToUpper(filepath.Clean(path))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	return strings.ToUpper(filepath.Clean(absolute))
}

func traceMutexName(path string) string {
	digest := sha256.Sum256([]byte(canonicalTraceSinkIdentity(path)))
	return `Local\shell-picker-trace-` + hex.EncodeToString(digest[:])
}

func newWindowsTraceRecordLock(path string) (*windowsTraceRecordLock, error) {
	name, err := windows.UTF16PtrFromString(traceMutexName(path))
	if err != nil {
		return nil, fmt.Errorf("create trace record lock name: %w", err)
	}
	handle, err := windows.CreateMutex(nil, true, name)
	created := true
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		created = false
		err = nil
	}
	if err != nil || handle == 0 {
		if err == nil {
			err = errors.New("invalid trace record lock handle")
		}
		return nil, fmt.Errorf("create trace record lock: %w", err)
	}
	return &windowsTraceRecordLock{handle: handle, created: created,
		wait:    windows.WaitForSingleObject,
		release: windows.ReleaseMutex, close: windows.CloseHandle}, nil
}
