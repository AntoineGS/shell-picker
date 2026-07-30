package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func fixedSessionID() [16]byte {
	return [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f'}
}

func TestTraceWritesStableRedactedJSONL(t *testing.T) {
	var output bytes.Buffer
	trace := NewTrace(&output, fixedSessionID())
	path := []byte("/home/user/private project")
	if err := trace.Event(TraceEvent{Name: "generation.publish", Generation: 2, CandidateCount: 4,
		Outcome: "ok", Path: path}); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(output.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("trace is not one JSONL record: %q", output.Bytes())
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), &record); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"schema": float64(1), "session": "sha256:9f9f5111f7b27a78", "event": "generation.publish",
		"generation": float64(2), "candidate_count": float64(4), "outcome": "ok",
		"path": "sha256:267273ff1ea6a14f",
	}
	for key, value := range want {
		if record[key] != value {
			t.Fatalf("record[%q]=%v want %v; record=%v", key, record[key], value, record)
		}
	}
	if _, ok := record["time"]; !ok {
		t.Fatalf("trace lacks time: %v", record)
	}
	for key := range record {
		if !map[string]bool{"schema": true, "time": true, "session": true, "event": true, "generation": true,
			"candidate_count": true, "renderer": true, "outcome": true, "path": true}[key] {
			t.Fatalf("unstable trace field %q in %v", key, record)
		}
	}
	text := output.String()
	for _, forbidden := range []string{string(path), "private project", "token", "query", "record"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("trace leaked %q: %s", forbidden, text)
		}
	}
}

func TestTraceAcceptsOnlyTask19EventsAndBoundedFields(t *testing.T) {
	valid := []TraceEvent{
		{Name: "session.start", Outcome: "cp"}, {Name: "generation.publish", Generation: 1, Outcome: "ok"},
		{Name: "fzf.start", Outcome: "ok"}, {Name: "fzf.exit", Outcome: "aborted"},
		{Name: "callback.event", Outcome: "en"}, {Name: "callback.load", Generation: 2, Outcome: "ok"},
		{Name: "preview.dispatch", Renderer: "native", Outcome: "ok"}, {Name: "session.close", Outcome: "accepted"},
	}
	var output bytes.Buffer
	trace := NewTrace(&output, fixedSessionID())
	for _, event := range valid {
		if err := trace.Event(event); err != nil {
			t.Fatalf("valid event %+v: %v", event, err)
		}
	}
	before := output.Len()
	invalid := []TraceEvent{
		{Name: "generation.start", Outcome: "ok"}, {Name: "callback.event", Outcome: "execute(evil)"},
		{Name: "preview.dispatch", Renderer: strings.Repeat("x", 65), Outcome: "ok"},
		{Name: "session.close", Outcome: strings.Repeat("x", 65)},
	}
	for _, event := range invalid {
		if err := trace.Event(event); err == nil {
			t.Fatalf("invalid event accepted: %+v", event)
		}
	}
	if output.Len() != before {
		t.Fatalf("invalid event reached writer: %q", output.Bytes()[before:])
	}
}

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
