package app

import (
	"context"
	"io"
	"sync"
)

type initialStreamRun struct {
	cancel   context.CancelCauseFunc
	writer   *io.PipeWriter
	done     chan error
	once     sync.Once
	waitOnce sync.Once
	result   error
}

func startInitialStream(parent context.Context, writer *io.PipeWriter, run func(context.Context, io.Writer) error) *initialStreamRun {
	ctx, cancel := context.WithCancelCause(parent)
	stream := &initialStreamRun{cancel: cancel, writer: writer, done: make(chan error, 1)}
	go func() {
		err := run(ctx, writer)
		if err != nil {
			_ = writer.CloseWithError(err)
		} else {
			_ = writer.Close()
		}
		stream.done <- err
	}()
	return stream
}

func (stream *initialStreamRun) stop(cause error) {
	stream.once.Do(func() {
		if cause != nil {
			stream.cancel(cause)
			_ = stream.writer.CloseWithError(cause)
		}
	})
}

func (stream *initialStreamRun) wait() error {
	stream.waitOnce.Do(func() {
		stream.result = <-stream.done
		stream.cancel(stream.result)
	})
	return stream.result
}
