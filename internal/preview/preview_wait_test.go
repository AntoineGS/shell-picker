package preview

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const previewWaitTimeout = 5 * time.Second

func previewWaitContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), previewWaitTimeout)
	t.Cleanup(cancel)
	return ctx
}

func awaitPreview[T any](t *testing.T, values <-chan T, operation string) T {
	t.Helper()
	return awaitPreviewContext(t, previewWaitContext(t), values, operation)
}

func awaitPreviewContext[T any](t *testing.T, ctx context.Context, values <-chan T, operation string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s: %v", operation, ctx.Err())
		var zero T
		return zero
	}
}

func awaitPreviewFile(t *testing.T, path, operation string) []byte {
	t.Helper()
	ctx := previewWaitContext(t)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) != 0 {
			return data
		}
		lastErr = err
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s at %s: %v (last read error: %v)", operation, path, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

type barrierReader struct {
	reader            *strings.Reader
	started, proceed  chan struct{}
	once, releaseOnce sync.Once
	ctx               context.Context
	operation         string
	waitErr           error
}

func newBarrierReader(t *testing.T, value string) *barrierReader {
	t.Helper()
	operation := fmt.Sprintf("cache write barrier for %q", value)
	reader := &barrierReader{
		reader:    strings.NewReader(value),
		started:   make(chan struct{}),
		proceed:   make(chan struct{}),
		ctx:       previewWaitContext(t),
		operation: operation,
	}
	t.Cleanup(reader.release)
	return reader
}

func (reader *barrierReader) release() {
	reader.releaseOnce.Do(func() { close(reader.proceed) })
}

func (reader *barrierReader) Read(data []byte) (int, error) {
	reader.once.Do(func() {
		close(reader.started)
		select {
		case <-reader.proceed:
		case <-reader.ctx.Done():
			reader.waitErr = fmt.Errorf("%s was not released: %w", reader.operation, reader.ctx.Err())
		}
	})
	if reader.waitErr != nil {
		return 0, reader.waitErr
	}
	return reader.reader.Read(data)
}
