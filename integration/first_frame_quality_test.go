package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFirstFrameCallbackCountsUseCompleteTraceAndMeaningfulFrameBoundary(t *testing.T) {
	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	const session = "sha256:0123456789abcdef"
	events := []traceEvent{
		{Schema: 2, Time: base.Format(time.RFC3339Nano), Session: session, Event: "session.start", Outcome: "cd"},
		{Schema: 2, Time: base.Add(500 * time.Microsecond).Format(time.RFC3339Nano), Session: session, Event: "callback.info.start", Outcome: "started"},
		{Schema: 2, Time: base.Add(time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "callback.info", Outcome: "ok"},
		{Schema: 2, Time: base.Add(2 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "preview.finished", Renderer: "eza", Outcome: "ok", ChildStarts: 1},
		{Schema: 2, Time: base.Add(2500 * time.Microsecond).Format(time.RFC3339Nano), Session: session, Event: "callback.display.start", Outcome: "started"},
		{Schema: 2, Time: base.Add(3 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "callback.display", Outcome: "ok"},
		{Schema: 2, Time: base.Add(4 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "session.close", Outcome: "accepted"},
	}
	counts, err := countFirstFrameCallbackInvocationsThrough(events, base.Add(2*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if counts.info != 1 || counts.display != 0 || counts.callbackTotal() != 1 {
		t.Fatalf("callback counts through meaningful frame=%+v, want only the pre-boundary callback", counts)
	}
}

func TestFirstFrameMeaningfulFrameCapturesWallAndMonotonicTimestamps(t *testing.T) {
	stamp := captureFirstFrameTimestamp()
	if stamp.wall.IsZero() || stamp.monotonic.IsZero() {
		t.Fatalf("meaningful frame timestamp=%+v, want wall and monotonic readings", stamp)
	}
	if stamp.wall.Location() != time.UTC {
		t.Fatalf("meaningful frame wall timestamp location=%v, want UTC", stamp.wall.Location())
	}
}

func TestFirstFrameMeaningfulFrameDurationUsesMonotonicClockAcrossWallJump(t *testing.T) {
	sample := firstFrameSampleFixture()
	started := time.Now()
	wallBase := started.UTC()
	for index := range sample.events {
		sample.events[index].Time = wallBase.Add(time.Duration(index) * time.Millisecond).Format(time.RFC3339Nano)
	}
	sample.started = started
	sample.firstConPTYByte = started.Add(time.Millisecond)
	sample.firstMeaningfulFrame = firstFrameTimestamp{wall: wallBase.Add(12 * time.Millisecond), monotonic: started.Add(25 * time.Millisecond)}
	metrics, err := measureFirstFrameTrace(sample.events, sample.started, sample.firstConPTYByte, sample.firstMeaningfulFrame)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.meaningfulFrameUS != 25_000 {
		t.Fatalf("meaningful-frame duration=%d us, want monotonic 25000 us despite wall cutoff=%s", metrics.meaningfulFrameUS, sample.firstMeaningfulFrame.wall)
	}
	counts, err := countFirstFrameCallbackInvocationsThrough(sample.events, sample.firstMeaningfulFrame.wall)
	if err != nil {
		t.Fatal(err)
	}
	if counts.preview != 1 || counts.load != 0 {
		t.Fatalf("wall trace cutoff counts=%+v, want preview before and load after cutoff", counts)
	}
}

func TestFirstFrameMakefileKeepsReproducibleFlagsScopedToFirstFrameTarget(t *testing.T) {
	data, err := os.ReadFile("../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	build := firstFrameMakefileSection(text, "build:", "install:")
	install := firstFrameMakefileSection(text, "install:", "windows-native:")
	dedicated := firstFrameMakefileSection(text, "performance-dedicated:", "performance-first-frame:")
	firstFrame := firstFrameMakefileSection(text, "performance-first-frame:", "cross-build:")
	if !strings.Contains(build, "go build -trimpath -o bin/shell-picker ./cmd/shell-picker") || strings.Contains(build, "-buildvcs=false") || strings.Contains(build, "-ldflags=-buildid=") {
		t.Fatalf("ordinary build target changed: %s", build)
	}
	if !strings.Contains(install, "go install -trimpath ./cmd/shell-picker") || strings.Contains(install, "-buildvcs=false") || strings.Contains(install, "-ldflags=-buildid=") {
		t.Fatalf("ordinary install target changed: %s", install)
	}
	if strings.Contains(dedicated, "-buildvcs=false") || strings.Contains(dedicated, "-ldflags=-buildid=") {
		t.Fatalf("ordinary dedicated target contains reproducible-only flags: %s", dedicated)
	}
	for _, required := range []string{"verify-first-frame-build.ps1", "TestDedicatedBaseline", "TestDedicatedFirstFrameTargets"} {
		if !strings.Contains(firstFrame, required) {
			t.Fatalf("first-frame target lacks %q: %s", required, firstFrame)
		}
	}
}

func TestFirstFramePreviewBoundaryUsesCompleteVocabulary(t *testing.T) {
	legacyJSON := "through_preview_" + "renderer_" + "start"
	legacyField := "Preview" + "Start"
	legacyProcessField := "processPreview" + "Start"
	legacyCallbackField := "callbackPreview" + "Start"
	paths, err := filepath.Glob("first_frame*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path == "first_frame_quality_test.go" {
			continue
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), legacyJSON) || strings.Contains(string(data), legacyField) || strings.Contains(string(data), legacyProcessField) || strings.Contains(string(data), legacyCallbackField) {
			t.Fatalf("%s retains the renderer-start boundary vocabulary", path)
		}
	}
	harness, err := os.ReadFile("first_frame_harness_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(harness), "through_preview_complete") {
		t.Fatal("first-frame JSON does not expose the preview-complete boundary")
	}
}

func TestFirstFrameSourceFilesStayBelowFiveHundredLines(t *testing.T) {
	paths, err := filepath.Glob("first_frame*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		lines := 0
		if len(data) != 0 {
			lines = strings.Count(string(data), "\n")
			if data[len(data)-1] != '\n' {
				lines++
			}
		}
		if lines >= 500 {
			t.Fatalf("%s has %d lines; first-frame files must stay below 500", path, lines)
		}
	}
}

func firstFrameMakefileSection(text, start, end string) string {
	startIndex := strings.Index(text, start)
	if startIndex < 0 {
		return ""
	}
	text = text[startIndex:]
	if endIndex := strings.Index(text, end); endIndex >= 0 {
		text = text[:endIndex]
	}
	return text
}
