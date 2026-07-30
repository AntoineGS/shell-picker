//go:build !windows

package app

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func openTraceSink(path string) (io.WriteCloser, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
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
		if err := unix.Ftruncate(fd, 0); err != nil {
			return fail(err)
		}
	}
	return file, nil
}
