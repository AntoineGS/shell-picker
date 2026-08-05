package integration

import (
	"errors"
	"testing"
)

func TestTraceReportsOneWriteFailureThenDisables(t *testing.T) {
	writer := &failingTraceWriter{}
	trace := NewTrace(writer, fixedSessionID())
	first := trace.Event(TraceEvent{Name: "session.start", Outcome: "cp"})
	if !errors.Is(first, errTraceWriteFixture) {
		t.Fatalf("first failure=%v", first)
	}
	if err := trace.Event(TraceEvent{Name: "session.close", Outcome: "error"}); err != nil {
		t.Fatalf("disabled trace returned another diagnostic: %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("disabled trace wrote %d times", writer.calls)
	}
}

var errTraceWriteFixture = errors.New("trace writer fixture failed")

type failingTraceWriter struct{ calls int }

func (writer *failingTraceWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, errTraceWriteFixture
}
