package integration

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

func TestDedicatedTraceCountersRequireMeasuredExitAndNoLiveRemainder(t *testing.T) {
	valid := []traceEvent{{Event: "zoxide.enrichment", Generation: 1, Outcome: "published", ZoxideOutcome: "ok", ZoxideAttempts: 1, ZoxideStarts: 1,
		ZoxideExits: 1, ZoxideProcesses: 1, ZoxideLive: 0, ZoxideMaxLive: 1}}
	counters, err := traceBenchmarkCounters(valid, 0)
	if err != nil || counters.ZoxideExits != 1 || counters.ZoxideProcesses != 1 {
		t.Fatalf("valid counters=%+v err=%v", counters, err)
	}
	mutated := []traceEvent{{Event: "zoxide.enrichment", Generation: 1, Outcome: "published", ZoxideOutcome: "ok", ZoxideAttempts: 1, ZoxideStarts: 1,
		ZoxideExits: 0, ZoxideProcesses: 1, ZoxideLive: 1, ZoxideMaxLive: 1}}
	if _, err := traceBenchmarkCounters(mutated, 0); err == nil {
		t.Fatal("missing measured exit and live remainder accepted")
	}
}

func TestDedicatedTraceSampleUsesTraceMarkersForStartupEnrichmentAndLifecycle(t *testing.T) {
	events := dedicatedTraceFixture()
	measurement, err := parseDedicatedTraceSample(events, true)
	if err != nil {
		t.Fatal(err)
	}
	if measurement.StartupDuration != 10*time.Millisecond {
		t.Fatalf("startup=%v", measurement.StartupDuration)
	}
	if measurement.EnrichmentDuration == nil || *measurement.EnrichmentDuration != 150*time.Millisecond {
		t.Fatalf("enrichment=%v", measurement.EnrichmentDuration)
	}
	if measurement.LifecycleDuration != 200*time.Millisecond {
		t.Fatalf("lifecycle=%v", measurement.LifecycleDuration)
	}
}

func TestWriteDedicatedTracePreservesPerSampleJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "startup-local-only-001.trace.jsonl")
	want := dedicatedTraceFixture()
	if err := writeDedicatedTrace(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != len(want) {
		t.Fatalf("trace lines=%d want %d; data=%q", len(lines), len(want), data)
	}
	for index, line := range lines {
		got, err := decodeTraceEvent(line)
		if err != nil {
			t.Fatalf("decode trace line %d: %v", index, err)
		}
		if got.Event != want[index].Event || got.Time != want[index].Time || got.Outcome != want[index].Outcome {
			t.Fatalf("trace line %d=%+v want event/time/outcome %s/%s/%s", index, got, want[index].Event, want[index].Time, want[index].Outcome)
		}
	}
}

func writeDedicatedTrace(path string, events []traceEvent) error {
	var data bytes.Buffer
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		data.Write(line)
		data.WriteByte('\n')
	}
	return os.WriteFile(path, data.Bytes(), 0o600)
}

func TestDedicatedBlockedSourceKeepsStartupBeforeDelayedEnrichment(t *testing.T) {
	events := dedicatedTraceFixture()
	measurement, err := parseDedicatedTraceSample(events, true)
	if err != nil {
		t.Fatal(err)
	}
	if measurement.EnrichmentDuration == nil || measurement.StartupDuration >= *measurement.EnrichmentDuration {
		t.Fatalf("startup=%v enrichment=%v; blocked source must finish later", measurement.StartupDuration, measurement.EnrichmentDuration)
	}
	if measurement.LifecycleDuration < *measurement.EnrichmentDuration {
		t.Fatalf("lifecycle=%v enrichment=%v; joined lifecycle must include source", measurement.LifecycleDuration, measurement.EnrichmentDuration)
	}
}

func TestDedicatedTraceSampleRejectsMalformedSessionMarkersAndTerminal(t *testing.T) {
	base := dedicatedTraceFixture()
	cases := []struct {
		name   string
		mutate func([]traceEvent) []traceEvent
	}{
		{name: "duplicate session start", mutate: func(events []traceEvent) []traceEvent {
			return append(events, events[0])
		}},
		{name: "missing fzf start", mutate: func(events []traceEvent) []traceEvent {
			return removeTraceEvent(events, "fzf.start")
		}},
		{name: "duplicate enrichment", mutate: func(events []traceEvent) []traceEvent {
			for _, event := range events {
				if event.Event == "zoxide.enrichment" {
					return append(events, event)
				}
			}
			return events
		}},
		{name: "reversed timestamp", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "fzf.start" {
					events[index].Time = traceFixtureTimestamp(events[0], -time.Nanosecond)
				}
			}
			return events
		}},
		{name: "mismatched session", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "fzf.start" {
					events[index].Session = "sha256:fedcba9876543210"
				}
			}
			return events
		}},
		{name: "non-cd session start", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "session.start" {
					events[index].Outcome = "cp"
				}
			}
			return events
		}},
		{name: "unsupported schema", mutate: func(events []traceEvent) []traceEvent {
			events[1].Schema = 1
			return events
		}},
		{name: "missing required enrichment", mutate: func(events []traceEvent) []traceEvent {
			return removeTraceEvent(events, "zoxide.enrichment")
		}},
		{name: "invalid terminal generation", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "zoxide.enrichment" {
					events[index].Generation = 2
				}
			}
			return events
		}},
		{name: "pending terminal outcome", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "zoxide.enrichment" {
					events[index].ZoxideOutcome = "pending"
				}
			}
			return events
		}},
		{name: "live terminal process", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "zoxide.enrichment" {
					events[index].ZoxideLive = 1
				}
			}
			return events
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := parseDedicatedTraceSample(testCase.mutate(cloneTraceEvents(base)), true); err == nil {
				t.Fatal("malformed trace accepted")
			}
		})
	}
}

func TestDedicatedTraceParserRejectsAdversarialConsumedRecords(t *testing.T) {
	base := dedicatedTraceFixture()
	cases := []struct {
		name   string
		mutate func([]traceEvent) []traceEvent
	}{
		{name: "error fzf start", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "fzf.start" {
					events[index].Outcome = "error"
				}
			}
			return events
		}},
		{name: "malformed redacted session", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "fzf.start" {
					events[index].Session = "sha256:not-a-redaction"
				}
			}
			return events
		}},
		{name: "invalid fzf fields", mutate: func(events []traceEvent) []traceEvent {
			for index := range events {
				if events[index].Event == "fzf.start" {
					events[index].ZoxideAttempts = 1
				}
			}
			return events
		}},
		{name: "unknown event", mutate: func(events []traceEvent) []traceEvent {
			return append(events[:1], append([]traceEvent{{Schema: integrationpkg.TraceSchema, Time: traceFixtureTimestamp(events[0], 5*time.Millisecond),
				Session: events[0].Session, Event: "trace.unknown", Outcome: "ok"}}, events[1:]...)...)
		}},
		{name: "intervening timestamp reversal", mutate: func(events []traceEvent) []traceEvent {
			return append(events[:2], append([]traceEvent{{Schema: integrationpkg.TraceSchema, Time: traceFixtureTimestamp(events[1], -5*time.Millisecond),
				Session: events[0].Session, Event: "generation.start", Generation: 1, Outcome: "ok"}}, events[2:]...)...)
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := parseDedicatedTraceSample(testCase.mutate(cloneTraceEvents(base)), true); err == nil {
				t.Fatal("adversarial trace accepted")
			}
		})
	}
}

func TestDedicatedTraceSampleAllowsAtMostOneOptionalEnrichment(t *testing.T) {
	events := removeTraceEvent(dedicatedTraceFixture(), "zoxide.enrichment")
	measurement, err := parseDedicatedTraceSample(events, false)
	if err != nil {
		t.Fatal(err)
	}
	if measurement.EnrichmentDuration != nil {
		t.Fatalf("optional enrichment=%v", measurement.EnrichmentDuration)
	}
}

func dedicatedTraceFixture() []traceEvent {
	const session = "sha256:0123456789abcdef"
	start := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	return []traceEvent{
		{Schema: integrationpkg.TraceSchema, Time: start.Format(time.RFC3339Nano), Session: session, Event: "session.start", Outcome: "cd"},
		{Schema: integrationpkg.TraceSchema, Time: start.Add(10 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "fzf.start", Outcome: "ok"},
		{Schema: integrationpkg.TraceSchema, Time: start.Add(150 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "zoxide.enrichment", Generation: 1,
			CandidateCount: 1, Outcome: "published", ZoxidePolicy: "cached", ZoxideOutcome: "ok", ZoxideAttempts: 1, ZoxideStarts: 1,
			ZoxideExits: 1, ZoxideProcesses: 1, ZoxideMaxLive: 1},
		{Schema: integrationpkg.TraceSchema, Time: start.Add(200 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "session.close", Outcome: "aborted"},
	}
}

func traceFixtureTimestamp(event traceEvent, offset time.Duration) string {
	stamp, err := time.Parse(time.RFC3339Nano, event.Time)
	if err != nil {
		panic(err)
	}
	return stamp.Add(offset).Format(time.RFC3339Nano)
}

func removeTraceEvent(events []traceEvent, name string) []traceEvent {
	result := make([]traceEvent, 0, len(events))
	for _, event := range events {
		if event.Event != name {
			result = append(result, event)
		}
	}
	return result
}

func cloneTraceEvents(events []traceEvent) []traceEvent {
	return append([]traceEvent(nil), events...)
}

func TestDedicatedNavigationCountersUseMeasuredGeneration(t *testing.T) {
	events := []traceEvent{{Event: "generation.publish", Generation: 1, ZoxideOutcome: "ok", ZoxideAttempts: 1,
		ZoxideStarts: 1, ZoxideExits: 1, ZoxideProcesses: 1, ZoxideMaxLive: 1},
		{Event: "generation.publish", Generation: 2, ZoxideOutcome: "not-run"}}
	counters, err := traceBenchmarkCounters(events, 2)
	if err != nil || counters != (integrationpkg.BenchmarkCounters{}) {
		t.Fatalf("navigation counters=%+v err=%v", counters, err)
	}
	events[1].ZoxideOutcome = "ok"
	if _, err := traceBenchmarkCounters(events, 2); err == nil {
		t.Fatal("measured navigation generation without not-run outcome accepted")
	}
}

func TestDedicatedScenarioOutcomesKeepAsyncSourceOffNavigationCriticalPath(t *testing.T) {
	scenarios := dedicatedPickerScenarios(75 * time.Millisecond)
	want := map[string]string{
		"startup-local-only":           "ok",
		"startup-zoxide-present":       "ok",
		"startup-zoxide-missing":       "missing",
		"startup-zoxide-spawn-failure": "process-error",
		"startup-zoxide-blocked":       "ok",
		"startup-zoxide-timeout":       "timeout",
		"navigation-local-only":        "not-run",
		"preview-dispatch":             "ok",
	}
	if len(scenarios) != len(want) {
		t.Fatalf("scenario count=%d want %d", len(scenarios), len(want))
	}
	for _, scenario := range scenarios {
		if scenario.expectedZoxideOutcome != want[scenario.name] {
			t.Fatalf("%s outcome=%q want %q", scenario.name, scenario.expectedZoxideOutcome, want[scenario.name])
		}
		if scenario.name == "navigation-local-only" && scenario.expected != (integrationpkg.BenchmarkCounters{}) {
			t.Fatalf("navigation counters=%+v", scenario.expected)
		}
	}
}

func servePerformanceGate(listener net.Listener, release <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	connection, err := listener.Accept()
	if err != nil {
		return
	}
	defer connection.Close()
	<-release
	_, _ = connection.Write([]byte{1})
}

func traceBenchmarkZoxideOutcome(events []traceEvent, generation uint64) string {
	for _, event := range events {
		if generation == 0 && event.Event == "zoxide.enrichment" {
			return event.ZoxideOutcome
		}
		if generation != 0 && event.Event == "generation.publish" && event.Generation == generation {
			return event.ZoxideOutcome
		}
	}
	return ""
}
