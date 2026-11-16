// Package fsearchdb reads the saved FSearch 6.0 database format.
package fsearchdb

import (
	"errors"
	"time"
)

var (
	ErrUnsupported = errors.New("unsupported FSearch database format")
	ErrMalformed   = errors.New("malformed FSearch database")
	ErrBusy        = errors.New("FSearch database is busy")
)

// Entry is one saved file or directory. Path is kept as bytes because Unix
// filenames need not be valid UTF-8.
type Entry struct {
	Path    []byte
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// Database is an immutable decoded snapshot.
type Database struct {
	Entries []Entry
	SavedAt time.Time
}
