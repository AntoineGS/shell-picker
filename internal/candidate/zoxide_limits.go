package candidate

import (
	"context"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxZoxideOutputBytes matches the preview/tool output budget documented by
	// the protocol and bounds fresh timeout=0 source memory.
	MaxZoxideOutputBytes = 4 << 20
	// MaxZoxideRows matches the protocol candidate-count upper bound.
	MaxZoxideRows = 1_000_000
	// MaxZoxideRowBytes covers the maximum Windows extended path in UTF-8 and
	// remains comfortably above normal Unix path limits.
	MaxZoxideRowBytes = 128 << 10
)

var errZoxideOutputLimit = errors.New("zoxide output exceeds the configured limit")

type zoxideLimitWriter struct {
	sink       io.Writer
	cancel     context.CancelCauseFunc
	maxBytes   int
	maxRows    int
	maxRowSize int
	total      int
	rows       int
	rowBytes   int
	err        error
}

func newZoxideLimitWriter(sink io.Writer, maxBytes, maxRows, maxRowSize int, cancel context.CancelCauseFunc) *zoxideLimitWriter {
	if sink == nil {
		sink = io.Discard
	}
	return &zoxideLimitWriter{sink: sink, cancel: cancel, maxBytes: maxBytes, maxRows: maxRows, maxRowSize: maxRowSize}
}

func (writer *zoxideLimitWriter) Write(data []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	for index, value := range data {
		if writer.total+index >= writer.maxBytes {
			return writer.reject(data, index)
		}
		if value == '\n' {
			if writer.rows >= writer.maxRows {
				return writer.reject(data, index)
			}
			writer.rows++
			writer.rowBytes = 0
			continue
		}
		if writer.rows >= writer.maxRows || writer.rowBytes >= writer.maxRowSize {
			return writer.reject(data, index)
		}
		writer.rowBytes++
	}
	if len(data) == 0 {
		return 0, nil
	}
	if err := writer.writeSink(data); err != nil {
		writer.err = err
		return 0, err
	}
	writer.total += len(data)
	return len(data), nil
}

func (writer *zoxideLimitWriter) finalize() error {
	if writer.err != nil {
		return writer.err
	}
	if writer.rowBytes != 0 {
		if writer.rows >= writer.maxRows {
			_, err := writer.reject(nil, 0)
			return err
		}
		writer.rows++
	}
	return nil
}

func (writer *zoxideLimitWriter) reject(data []byte, accepted int) (int, error) {
	if accepted > 0 {
		if err := writer.writeSink(data[:accepted]); err != nil {
			writer.err = err
			return 0, err
		}
		writer.total += accepted
	}
	writer.err = errZoxideOutputLimit
	if writer.cancel != nil {
		writer.cancel(writer.err)
	}
	return accepted, writer.err
}

func (writer *zoxideLimitWriter) writeSink(data []byte) error {
	written, err := writer.sink.Write(data)
	if written < 0 || written > len(data) {
		return fmt.Errorf("zoxide output writer returned invalid count %d", written)
	}
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
