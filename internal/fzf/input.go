package fzf

import (
	"bytes"
	"errors"
	"io"
	"sync"
)

var (
	ErrInputClosed    = errors.New("fzf: input stream is closed")
	ErrNilPublication = errors.New("fzf: nil publication callback")
)

// InputStream is an appendable reader whose buffered input can be consumed by
// a process while another goroutine continues producing input.
type InputStream struct {
	mu     sync.Mutex
	ready  *sync.Cond
	buffer bytes.Buffer
	closed bool
	cause  error
}

func NewInputStream(initial []byte) *InputStream {
	stream := &InputStream{}
	stream.ready = sync.NewCond(&stream.mu)
	_, _ = stream.buffer.Write(initial)
	return stream
}

func (stream *InputStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()
	for stream.buffer.Len() == 0 && !stream.closed {
		stream.ready.Wait()
	}
	if stream.buffer.Len() > 0 {
		n, _ := stream.buffer.Read(p)
		return n, nil
	}
	if stream.cause != nil {
		return 0, stream.cause
	}
	return 0, io.EOF
}

func (stream *InputStream) Append(data []byte) error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return ErrInputClosed
	}
	if len(data) == 0 {
		return nil
	}
	_, _ = stream.buffer.Write(data)
	stream.ready.Broadcast()
	return nil
}

// AppendAfter serializes an application publication with stream closure. The
// callback runs while the stream is locked; its successful return is the sole
// permission to expose the copied batch to readers.
func (stream *InputStream) AppendAfter(data []byte, publish func() error) error {
	if publish == nil {
		return ErrNilPublication
	}
	batch := bytes.Clone(data)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return ErrInputClosed
	}
	if err := publish(); err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	_, _ = stream.buffer.Write(batch)
	stream.ready.Broadcast()
	return nil
}

func (stream *InputStream) Close() error {
	return stream.closeWithError(nil)
}

func (stream *InputStream) CloseWithError(cause error) error {
	return stream.closeWithError(cause)
}

func (stream *InputStream) closeWithError(cause error) error {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closed {
		return nil
	}
	stream.closed = true
	stream.cause = cause
	stream.ready.Broadcast()
	return nil
}
