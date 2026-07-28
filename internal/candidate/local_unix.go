//go:build !windows

package candidate

import (
	"context"
	"errors"
)

func enumerateDrives(context.Context) ([]Record, error) {
	return nil, errors.New("drive enumeration is unavailable on this platform")
}
