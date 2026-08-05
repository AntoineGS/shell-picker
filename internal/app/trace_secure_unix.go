//go:build !windows

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"golang.org/x/sys/unix"
)

const traceInitializationReadLimit = 4 << 10

func openTraceSink(path string, sessionID [16]byte) (io.WriteCloser, error) {
	return openTraceSinkWithExpectedSession(path, integrationpkg.RedactedSessionID(sessionID))
}

func openTraceSinkWithExpectedSession(path, expectedSession string) (io.WriteCloser, error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	fail := func(err error) (io.WriteCloser, error) {
		return nil, errors.Join(err, file.Close())
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fail(err)
	}
	kind := stat.Mode & unix.S_IFMT
	if kind != unix.S_IFREG && kind != unix.S_IFIFO {
		return fail(fmt.Errorf("trace sink is not a regular file or FIFO"))
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return fail(err)
	}
	if kind == unix.S_IFREG {
		needsTruncate, err := unixTraceFileNeedsTruncation(fd, expectedSession)
		if err != nil {
			return fail(err)
		}
		if needsTruncate {
			if err := unix.Ftruncate(fd, 0); err != nil {
				return fail(err)
			}
		}
	}
	return file, nil
}

func unixTraceFileNeedsTruncation(fd int, expectedSession string) (bool, error) {
	buffer := make([]byte, traceInitializationReadLimit)
	read, err := unix.Pread(fd, buffer, 0)
	if err != nil {
		return false, err
	}
	lineEnd := bytes.IndexByte(buffer[:read], '\n')
	if lineEnd < 0 {
		return true, nil
	}
	var record struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buffer[:lineEnd]), &record); err != nil {
		return true, nil
	}
	return record.Session != expectedSession, nil
}
