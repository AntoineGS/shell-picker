package preview

import (
	"errors"
	"io"
	"sync"
	"time"
)

var (
	ErrOutputLimit     = errors.New("preview: output limit exceeded")
	ErrInputLimit      = errors.New("preview: input limit exceeded")
	ErrArtifactLimit   = errors.New("preview: artifact limit exceeded")
	ErrArchiveEntries  = errors.New("preview: archive entry limit exceeded")
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
	writer *budgetWriter
}

func newCountingWriter(destination io.Writer, maximum int64) *countingWriter {
	budget := newOutputBudget(maximum, nil)
	return &countingWriter{writer: budget.writer(destination)}
}

func (writer *countingWriter) Write(data []byte) (int, error) {
	return writer.writer.Write(data)
}

type outputBudget struct {
	mu        sync.Mutex
	remaining int64
	written   int64
	exceeded  bool
	onLimit   func()
}

type budgetWriter struct {
	budget      *outputBudget
	destination io.Writer
}

func newOutputBudget(maximum int64, onLimit func()) *outputBudget {
	return &outputBudget{remaining: maximum, onLimit: onLimit}
}

func (budget *outputBudget) writer(destination io.Writer) *budgetWriter {
	if destination == nil {
		destination = io.Discard
	}
	return &budgetWriter{budget: budget, destination: destination}
}

func (writer *budgetWriter) Write(data []byte) (int, error) {
	budget := writer.budget
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.remaining <= 0 {
		budget.limitReachedLocked()
		return 0, ErrOutputLimit
	}
	allowed := len(data)
	limited := int64(allowed) > budget.remaining
	if limited {
		allowed = int(budget.remaining)
	}
	written, err := writer.destination.Write(data[:allowed])
	budget.remaining -= int64(written)
	budget.written += int64(written)
	if err != nil {
		return written, err
	}
	if limited && written == allowed {
		budget.limitReachedLocked()
		return written, ErrOutputLimit
	}
	return written, nil
}

func (budget *outputBudget) limitReachedLocked() {
	if budget.exceeded {
		return
	}
	budget.exceeded = true
	if budget.onLimit != nil {
		budget.onLimit()
	}
}

func (budget *outputBudget) status() (int64, bool) {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.written, budget.exceeded
}
