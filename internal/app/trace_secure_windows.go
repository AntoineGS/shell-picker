//go:build windows

package app

import (
	"io"
	"os"
)

func openTraceSink(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
}
