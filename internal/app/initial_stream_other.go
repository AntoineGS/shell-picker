//go:build !linux

package app

import (
	"context"
	"errors"
	"io"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fsearchdb"
)

func supportsInitialIndexedStream() bool { return false }

func (source *indexedSource) streamInitial(_ context.Context, _ candidate.BuildRequest, _ io.Writer) (*fsearchdb.Database, []candidate.Record, error) {
	return nil, nil, errors.New("initial indexed streaming is unsupported on this platform")
}
