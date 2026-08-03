package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type blockingTraceWriter struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
}

type chunkedTraceWriter struct {
	bytes.Buffer
	chunk int
}

func (writer *chunkedTraceWriter) Write(data []byte) (int, error) {
	if len(data) > writer.chunk {
		data = data[:writer.chunk]
		_, _ = writer.Buffer.Write(data)
		return len(data), io.ErrShortWrite
	}
	return writer.Buffer.Write(data)
}

func (*chunkedTraceWriter) Close() error { return nil }

func (writer *blockingTraceWriter) Write(data []byte) (int, error) {
	close(writer.started)
	<-writer.release
	return len(data), nil
}

func (writer *blockingTraceWriter) Close() error {
	close(writer.closed)
	return nil
}

func TestTraceCloseWaitsForInFlightEvent(t *testing.T) {
	writer := &blockingTraceWriter{started: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	trace := NewTrace(writer, fixedSessionID())
	eventDone := make(chan struct{})
	go func() {
		defer close(eventDone)
		if err := trace.Event(TraceEvent{Name: "session.start", Outcome: "cp"}); err != nil {
			t.Errorf("trace event: %v", err)
		}
	}()
	<-writer.started
	closeDone := make(chan struct{})
	go func() {
		_ = trace.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("trace closed while an event was writing")
	default:
	}
	close(writer.release)
	<-eventDone
	<-closeDone
	select {
	case <-writer.closed:
	default:
		t.Fatal("trace close did not close writer")
	}
}

func TestTraceCompletesShortJSONLWrites(t *testing.T) {
	writer := &chunkedTraceWriter{chunk: 3}
	trace := NewTrace(writer, fixedSessionID())
	if err := trace.Event(TraceEvent{Name: "session.start", Outcome: "cp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTraceRecord(writer.Bytes()); err != nil {
		t.Fatalf("short write produced invalid trace: %v; bytes=%q", err, writer.Bytes())
	}
}

func fixedSessionID() [16]byte {
	return [16]byte{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 'a', 'b', 'c', 'd', 'e', 'f'}
}

func TestTraceWritesOnlyCompatibleBoundedPerformanceFields(t *testing.T) {
	var output bytes.Buffer
	trace := NewTrace(&output, fixedSessionID())
	event := TraceEvent{Name: "generation.publish", Generation: 2, CandidateCount: 4, Path: []byte("/secret/path"),
		ZoxidePolicy: "cached", ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1, ZoxideProcesses: 1, ZoxideLive: 0, ZoxideMaxLive: 1,
		ActorQueueWait: 2 * time.Microsecond, LocalDuration: 3 * time.Microsecond,
		ZoxideDuration: 4 * time.Microsecond, ZoxideOutcome: "ok", TransformDuration: 5 * time.Microsecond,
		Outcome: "ok"}
	if err := trace.Event(event); err != nil {
		t.Fatal(err)
	}
	var record TraceRecord
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record.ZoxidePolicy != "cached" || record.ZoxideAttempts != 1 || record.ZoxideStarts != 1 || record.ZoxideExits != 1 ||
		record.ZoxideProcesses != 1 || record.ZoxideLive != 0 || record.ZoxideMaxLive != 1 ||
		record.ActorQueueWaitUS != 2 || record.LocalUS != 3 || record.ZoxideUS != 4 || record.ZoxideOutcome != "ok" || record.TransformUS != 5 {
		t.Fatalf("record=%+v", record)
	}

	before := output.Len()
	invalid := []TraceEvent{
		{Name: "fzf.exit", ZoxidePolicy: "cached", Outcome: "ok"},
		{Name: "generation.publish", Generation: 3, ZoxidePolicy: "stale", Outcome: "ok"},
		{Name: "generation.publish", Generation: 3, ZoxideAttempts: 0, ZoxideStarts: 1, Outcome: "ok"},
		{Name: "generation.publish", Generation: 3, ZoxideAttempts: 1, Outcome: "ok"},
		{Name: "preview.finished", Renderer: "native", ChildStarts: 1, MaxLiveChildren: 1, Outcome: "ok"},
		{Name: "preview.finished", Renderer: "bat", ChildStarts: 4, MaxLiveChildren: 1, Outcome: "ok"},
		{Name: "callback.load", Generation: 3, CallbackIPC: time.Microsecond, Outcome: "ok"},
	}
	for _, candidate := range invalid {
		if err := trace.Event(candidate); err == nil {
			t.Fatalf("accepted incompatible event: %+v", candidate)
		}
	}
	if output.Len() != before {
		t.Fatalf("invalid fields reached writer: %q", output.Bytes()[before:])
	}
}

func TestTraceAllowsBoundedLifecycleExtensionsWithoutInventedExitLatency(t *testing.T) {
	valid := []TraceEvent{
		{Name: "generation.start", Generation: 2, Outcome: "ok"},
		{Name: "generation.discard", Generation: 2, Outcome: "cancelled"},
		{Name: "preview.cancel", Renderer: "bat", Outcome: "cancelled"},
		{Name: "preview.exit", Renderer: "bat", ChildStarts: 1, MaxLiveChildren: 1, Outcome: "ok"},
	}
	var output bytes.Buffer
	trace := NewTrace(&output, fixedSessionID())
	for _, event := range valid {
		if err := trace.Event(event); err != nil {
			t.Fatalf("event=%+v error=%v", event, err)
		}
	}
	if strings.Contains(output.String(), "exit_latency") || strings.Contains(output.String(), "cancel_to_exit") {
		t.Fatalf("trace claims OS exit latency: %s", output.String())
	}
}

func TestTraceAcceptsPendingInitialPublicationAndStandaloneZoxideTerminals(t *testing.T) {
	valid := []TraceEvent{
		{Name: "generation.publish", Generation: 1, CandidateCount: 3, Outcome: "ok",
			ZoxidePolicy: "cached", ZoxideOutcome: "pending"},
		{Name: "zoxide.enrichment", Generation: 2, CandidateCount: 5, Outcome: "published",
			ZoxidePolicy: "cached", ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1,
			ZoxideProcesses: 1, ZoxideLive: 0, ZoxideMaxLive: 1,
			ZoxideDuration: time.Microsecond, ZoxideOutcome: "ok"},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "discarded",
			ZoxidePolicy: "cached", ZoxideOutcome: "cached"},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "failed",
			ZoxidePolicy: "fresh", ZoxideAttempts: 1, ZoxideOutcome: "missing"},
		{Name: "generation.discard", Generation: 3, Outcome: "error",
			ZoxidePolicy: "cached", ZoxideDuration: time.Microsecond, ZoxideOutcome: "not-run"},
	}
	var output bytes.Buffer
	trace := NewTrace(&output, fixedSessionID())
	for _, event := range valid {
		if err := trace.Event(event); err != nil {
			t.Fatalf("valid event %+v: %v", event, err)
		}
	}

	invalid := []TraceEvent{
		{Name: "zoxide.enrichment", Outcome: "published", CandidateCount: 1,
			ZoxidePolicy: "cached", ZoxideOutcome: "ok"},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "discarded", CandidateCount: 1,
			ZoxidePolicy: "cached", ZoxideOutcome: "ok"},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "failed", Path: []byte("/secret"),
			ZoxidePolicy: "cached", ZoxideOutcome: "missing"},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "failed"},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "failed",
			ZoxidePolicy: "cached", ZoxideOutcome: "pending"},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "failed",
			ZoxidePolicy: "cached", ZoxideOutcome: "not-run"},
		{Name: "generation.publish", Generation: 1, Outcome: "ok", ZoxidePolicy: "cached",
			ZoxideOutcome: "pending", ZoxideAttempts: 1},
		{Name: "generation.publish", Generation: 1, Outcome: "ok", ZoxidePolicy: "cached",
			ZoxideOutcome: "pending", ZoxideDuration: time.Microsecond},
		{Name: "zoxide.enrichment", Generation: 2, Outcome: "discarded", ZoxidePolicy: "cached",
			ZoxideOutcome: "cached", ActorQueueWait: time.Microsecond},
	}
	for _, event := range invalid {
		if err := trace.Event(event); err == nil {
			t.Fatalf("invalid event accepted: %+v", event)
		}
	}
}

func TestTraceSchemaAcceptsBalancedMultipleZoxideProcesses(t *testing.T) {
	valid := TraceEvent{Name: "zoxide.enrichment", Generation: 2, Outcome: "failed",
		ZoxidePolicy: "cached", ZoxideOutcome: "process-error", ZoxideAttempts: 2,
		ZoxideStarts: 2, ZoxideExits: 2, ZoxideProcesses: 2, ZoxideLive: 0, ZoxideMaxLive: 2}
	if err := validateTraceEvent(valid); err != nil {
		t.Fatalf("balanced multiple processes rejected: %v", err)
	}

	invalid := []TraceEvent{
		{ // starts must still be bounded by attempts
			Name: "zoxide.enrichment", Generation: 2, Outcome: "failed", ZoxidePolicy: "cached",
			ZoxideOutcome: "process-error", ZoxideAttempts: 1, ZoxideStarts: 2, ZoxideExits: 2, ZoxideProcesses: 2,
		},
		{ // exits and processes must remain balanced with starts
			Name: "zoxide.enrichment", Generation: 2, Outcome: "failed", ZoxidePolicy: "cached",
			ZoxideOutcome: "process-error", ZoxideAttempts: 2, ZoxideStarts: 2, ZoxideExits: 1, ZoxideProcesses: 2,
		},
	}
	for index, event := range invalid {
		if err := validateTraceEvent(event); err == nil {
			t.Errorf("unbalanced event %d accepted: %+v", index, event)
		}
	}
}

func TestTraceUsesValidatedInternalBoundaryTimestamp(t *testing.T) {
	var output bytes.Buffer
	boundary := time.Now().UTC().Add(-time.Millisecond).Truncate(time.Microsecond)
	trace := NewTrace(&output, fixedSessionID())
	if err := trace.Event(TraceEvent{Name: "callback.event", Outcome: "up", CallbackIPC: time.Millisecond, Timestamp: boundary}); err != nil {
		t.Fatal(err)
	}
	var record TraceRecord
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record.Time != boundary.Format(time.RFC3339Nano) {
		t.Fatalf("time=%q want %q", record.Time, boundary.Format(time.RFC3339Nano))
	}
	if err := trace.Event(TraceEvent{Name: "callback.event", Outcome: "up", Timestamp: time.Now().Add(time.Minute)}); err == nil {
		t.Fatal("future timestamp accepted")
	}
}

func TestTraceAuthorityNumericAndTimestampBounds(t *testing.T) {
	schema := traceSchemaAuthority()
	now := time.Now().UTC()
	valid := []TraceEvent{
		{Name: "generation.start", Generation: schema.GenerationMax, Outcome: "ok"},
		{Name: "generation.publish", CandidateCount: schema.CandidateCountMax, Outcome: "ok"},
		{Name: "preview.finished", Renderer: "bat", ChildStarts: schema.ChildStartsMax,
			MaxLiveChildren: schema.MaxLiveChildrenMax, Outcome: "ok"},
		{Name: "generation.publish", Outcome: "ok", ZoxidePolicy: schema.ZoxidePolicies[0],
			ZoxideOutcome: schema.ZoxideOutcomes[0], ZoxideAttempts: schema.ZoxideAttemptsMax},
		{Name: "generation.publish", Outcome: "ok", ActorQueueWait: schema.DurationMax,
			LocalDuration: schema.DurationMax, TransformDuration: schema.DurationMax},
		{Name: "callback.event", Outcome: "up", CallbackIPC: schema.DurationMax},
		{Name: "callback.load", Outcome: "ok", LoadDuration: schema.DurationMax},
		{Name: "generation.publish", Outcome: "ok", ZoxidePolicy: schema.ZoxidePolicies[0],
			ZoxideOutcome: schema.ZoxideOutcomes[0], ZoxideDuration: schema.DurationMax},
		{Name: "fzf.start", Outcome: "ok", Timestamp: now.Add(-schema.TimestampPastLimit)},
		{Name: "fzf.start", Outcome: "ok", Timestamp: now.Add(schema.TimestampFutureLimit)},
	}
	for _, event := range valid {
		if err := validateTraceEventWithSchema(event, schema, now); err != nil {
			t.Errorf("boundary event rejected: %+v: %v", event, err)
		}
	}
	invalid := []TraceEvent{
		{Name: "generation.publish", CandidateCount: schema.CandidateCountMin - 1, Outcome: "ok"},
		{Name: "generation.publish", CandidateCount: schema.CandidateCountMax + 1, Outcome: "ok"},
		{Name: "preview.finished", Renderer: "bat", ChildStarts: schema.ChildStartsMin - 1, Outcome: "ok"},
		{Name: "preview.finished", Renderer: "bat", ChildStarts: schema.ChildStartsMax + 1, Outcome: "ok"},
		{Name: "preview.finished", Renderer: "bat", MaxLiveChildren: schema.MaxLiveChildrenMin - 1, Outcome: "ok"},
		{Name: "preview.finished", Renderer: "bat", ChildStarts: 1,
			MaxLiveChildren: schema.MaxLiveChildrenMax + 1, Outcome: "ok"},
		{Name: "generation.publish", Outcome: "ok", ZoxidePolicy: schema.ZoxidePolicies[0],
			ZoxideOutcome: schema.ZoxideOutcomes[0], ZoxideAttempts: schema.ZoxideCounterMin - 1},
		{Name: "generation.publish", Outcome: "ok", ZoxidePolicy: schema.ZoxidePolicies[0],
			ZoxideOutcome: schema.ZoxideOutcomes[0], ZoxideAttempts: schema.ZoxideAttemptsMax + 1},
		{Name: "callback.event", Outcome: "up", CallbackIPC: schema.DurationMin - time.Nanosecond},
		{Name: "callback.event", Outcome: "up", CallbackIPC: schema.DurationMax + time.Nanosecond},
		{Name: "fzf.start", Outcome: "ok", Timestamp: now.Add(-schema.TimestampPastLimit - time.Nanosecond)},
		{Name: "fzf.start", Outcome: "ok", Timestamp: now.Add(schema.TimestampFutureLimit + time.Nanosecond)},
	}
	for _, event := range invalid {
		if err := validateTraceEventWithSchema(event, schema, now); err == nil {
			t.Errorf("out-of-bounds event accepted: %+v", event)
		}
	}
}

func TestTraceAlwaysEmitsRFC3339NanoUTCTimestamp(t *testing.T) {
	var output bytes.Buffer
	if err := NewTrace(&output, fixedSessionID()).Event(TraceEvent{Name: "fzf.start", Outcome: "ok"}); err != nil {
		t.Fatal(err)
	}
	var record TraceRecord
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, record.Time)
	if err != nil || record.Time == "" || parsed.Location() != time.UTC {
		t.Fatalf("time=%q parsed=%v error=%v", record.Time, parsed, err)
	}
}

func TestDecodeTraceRecordUsesProductionSchemaAuthority(t *testing.T) {
	valid := TraceRecord{Schema: TraceSchema, Time: "2026-07-31T10:00:00Z", Session: "sha256:0123456789abcdef",
		Event: "fzf.start", Outcome: "ok"}
	if err := ValidateTraceRecordAt(valid, time.Time{}); err != nil {
		t.Fatal(err)
	}
	invalid := []TraceRecord{
		{Schema: 1, Time: valid.Time, Session: valid.Session, Event: valid.Event, Outcome: valid.Outcome},
		{Schema: TraceSchema, Time: valid.Time, Session: "sha256:bad", Event: valid.Event, Outcome: valid.Outcome},
		{Schema: TraceSchema, Time: valid.Time, Session: valid.Session, Event: "fzf.start", Outcome: "error"},
		{Schema: TraceSchema, Time: valid.Time, Session: valid.Session, Event: "fzf.start", Outcome: "ok", ZoxideAttempts: 1},
	}
	for _, record := range invalid {
		if err := ValidateTraceRecordAt(record, time.Time{}); err == nil {
			t.Fatalf("invalid record accepted: %+v", record)
		}
	}
	legacy := valid
	legacy.Schema = 1
	if err := ValidateTraceRecordAt(legacy, time.Time{}); err == nil || !strings.Contains(err.Error(), "unsupported schema 1") {
		t.Fatalf("legacy schema error=%v", err)
	}
	legacyData, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTraceRecordAt(legacyData, time.Time{}); err == nil || !strings.Contains(err.Error(), "unsupported schema 1") {
		t.Fatalf("legacy parser error=%v", err)
	}
	data := []byte(`{"schema":1,"time":"2026-07-31T10:00:00Z","session":"sha256:0123456789abcdef","event":"fzf.start","outcome":"ok","unexpected":true}`)
	if _, err := DecodeTraceRecordAt(data, time.Time{}); err == nil {
		t.Fatal("unknown trace field accepted")
	}
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
		"schema": float64(2), "session": "sha256:9f9f5111f7b27a78", "event": "generation.publish",
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
			"candidate_count": true, "renderer": true, "child_starts": true, "max_live_children": true,
			"outcome": true, "path": true, "zoxide_policy": true, "zoxide_attempts": true, "zoxide_starts": true,
			"zoxide_exits": true, "zoxide_processes": true, "zoxide_live": true,
			"zoxide_max_live": true, "actor_queue_wait_us": true, "callback_ipc_us": true, "local_us": true,
			"zoxide_us": true, "zoxide_outcome": true, "transform_us": true, "load_us": true}[key] {
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
		{Name: "callback.event", Outcome: "en"}, {Name: "callback.event", Outcome: "rs"},
		{Name: "callback.load", Generation: 2, Outcome: "ok"},
		{Name: "preview.dispatch", Renderer: "native", Outcome: "ok"},
		{Name: "preview.finished", Renderer: "native", Outcome: "ok"},
		{Name: "preview.finished", Renderer: "eza-fallback", Outcome: "error"}, {Name: "session.close", Outcome: "accepted"},
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
		{Name: "callback.event", Outcome: "execute(evil)"},
		{Name: "preview.dispatch", Renderer: strings.Repeat("x", 65), Outcome: "ok"},
		{Name: "preview.finished", Renderer: "native", Outcome: "execute(evil)"},
		{Name: "preview.finished", Renderer: "unknown", Outcome: "ok"},
		{Name: "preview.finished", Renderer: "native", Outcome: "ok", Path: []byte("token-query-record")},
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
