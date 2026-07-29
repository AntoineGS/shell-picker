package preview

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCountingWriterReturnsOutputLimitWithoutExceedingBound(t *testing.T) {
	var destination bytes.Buffer
	writer := newCountingWriter(&destination, 4)
	n, err := writer.Write([]byte("abcdef"))
	if n != 4 || !errors.Is(err, ErrOutputLimit) || destination.String() != "abcd" {
		t.Fatalf("n=%d err=%v output=%q", n, err, destination.String())
	}
	if n, err = writer.Write([]byte("z")); n != 0 || !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("second write n=%d err=%v", n, err)
	}
}

func TestNormalizedLimitsRestoreSecurityDefaults(t *testing.T) {
	got := normalizedLimits(Limits{})
	if got != DefaultLimits {
		t.Fatalf("got %+v want %+v", got, DefaultLimits)
	}
}

func TestOutputBudgetAggregatesConcurrentDestinations(t *testing.T) {
	var first, second bytes.Buffer
	var limitCalls atomic.Int32
	budget := newOutputBudget(10, func() { limitCalls.Add(1) })
	writers := []*budgetWriter{budget.writer(&first), budget.writer(&second)}
	errorsSeen := make(chan error, len(writers))
	var group sync.WaitGroup
	for _, writer := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := writer.Write([]byte("12345678"))
			errorsSeen <- err
		}()
	}
	group.Wait()
	close(errorsSeen)
	limited := false
	for err := range errorsSeen {
		limited = limited || errors.Is(err, ErrOutputLimit)
	}
	written, exceeded := budget.status()
	if written != 10 || first.Len()+second.Len() != 10 || !exceeded || !limited || limitCalls.Load() != 1 {
		t.Fatalf("written=%d destinations=%d exceeded=%v limited=%v callbacks=%d", written,
			first.Len()+second.Len(), exceeded, limited, limitCalls.Load())
	}
}
