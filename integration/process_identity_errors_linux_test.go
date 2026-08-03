//go:build linux

package integration

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isTransientProcessIdentityError(err error) bool {
	return errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) || errors.Is(err, errProcessIdentityChanged)
}
