package preview

import (
	"errors"
	"io"
	"time"
)

var (
	ErrOutputLimit     = errors.New("preview: output limit exceeded")
	ErrInputLimit      = errors.New("preview: input limit exceeded")
	ErrArtifactLimit   = errors.New("preview: artifact limit exceeded")
	ErrPathNotAbsolute = errors.New("preview: path is not an absolute filesystem candidate")
)

type Limits struct {
	Deadline                    time.Duration
	MaxOutputBytes              int64
	MaxInternalInputBytes       int64
	MaxInternalLines            int
	MaxArchiveEntries           int
	MaxArchiveDecompressedBytes int64
	MaxArtifactBytes            int64
}

var DefaultLimits = Limits{
	Deadline: 10 * time.Second, MaxOutputBytes: 4 << 20, MaxInternalInputBytes: 4 << 20,
	MaxInternalLines: 10_000, MaxArchiveEntries: 100, MaxArchiveDecompressedBytes: 4 << 20,
	MaxArtifactBytes: 64 << 20,
}

func normalizedLimits(limits Limits) Limits {
	if limits.Deadline <= 0 {
		limits.Deadline = DefaultLimits.Deadline
	}
	if limits.MaxOutputBytes <= 0 {
		limits.MaxOutputBytes = DefaultLimits.MaxOutputBytes
	}
	if limits.MaxInternalInputBytes <= 0 {
		limits.MaxInternalInputBytes = DefaultLimits.MaxInternalInputBytes
	}
	if limits.MaxInternalLines <= 0 {
		limits.MaxInternalLines = DefaultLimits.MaxInternalLines
	}
	if limits.MaxArchiveEntries <= 0 {
		limits.MaxArchiveEntries = DefaultLimits.MaxArchiveEntries
	}
	if limits.MaxArchiveDecompressedBytes <= 0 {
		limits.MaxArchiveDecompressedBytes = DefaultLimits.MaxArchiveDecompressedBytes
	}
	if limits.MaxArtifactBytes <= 0 {
		limits.MaxArtifactBytes = DefaultLimits.MaxArtifactBytes
	}
	return limits
}

type countingWriter struct {
	destination io.Writer
	remaining   int64
	written     int64
	exceeded    bool
	onLimit     func()
}

func newCountingWriter(destination io.Writer, maximum int64) *countingWriter {
	if destination == nil {
		destination = io.Discard
	}
	return &countingWriter{destination: destination, remaining: maximum}
}

func (writer *countingWriter) Write(data []byte) (int, error) {
	if int64(len(data)) <= writer.remaining {
		n, err := writer.destination.Write(data)
		writer.remaining -= int64(n)
		writer.written += int64(n)
		return n, err
	}
	if writer.remaining <= 0 {
		writer.limitReached()
		return 0, ErrOutputLimit
	}
	allowed := int(writer.remaining)
	n, err := writer.destination.Write(data[:allowed])
	writer.remaining -= int64(n)
	writer.written += int64(n)
	if err != nil {
		return n, err
	}
	writer.limitReached()
	return n, ErrOutputLimit
}

func (writer *countingWriter) limitReached() {
	if writer.exceeded {
		return
	}
	writer.exceeded = true
	if writer.onLimit != nil {
		writer.onLimit()
	}
}
