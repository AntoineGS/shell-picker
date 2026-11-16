//go:build linux

package fsearchdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxDatabaseBytes = 8 << 30

// Load reads one opened database snapshot while holding a nonblocking shared
// advisory lock for the entire read and decode operation.
func Load(ctx context.Context, path string) (*Database, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: nil context", ErrMalformed)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open FSearch database %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat FSearch database %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: database is not a regular file", ErrMalformed)
	}
	if info.Size() < 0 || info.Size() > maxDatabaseBytes {
		return nil, fmt.Errorf("%w: database size exceeds limit", ErrMalformed)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("lock FSearch database: %w", ErrBusy)
		}
		return nil, fmt.Errorf("lock FSearch database: %w", err)
	}
	data := make([]byte, int(info.Size()))
	for pos := 0; pos < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := f.Read(data[pos:min(pos+(1<<20), len(data))])
		pos += n
		if readErr != nil {
			if readErr == io.EOF && pos == len(data) {
				break
			}
			return nil, fmt.Errorf("read FSearch database %s: %w", path, readErr)
		}
		if n == 0 {
			return nil, fmt.Errorf("%w: short database read", ErrMalformed)
		}
	}
	db, err := Decode(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("decode FSearch database %s: %w", path, err)
	}
	db.SavedAt = info.ModTime()
	return db, nil
}
