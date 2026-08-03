//go:build windows

package integration

import (
	"errors"

	"golang.org/x/sys/windows"
)

func isTransientProcessIdentityError(err error) bool {
	return errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_NOT_FOUND) || errors.Is(err, errProcessIdentityChanged)
}
