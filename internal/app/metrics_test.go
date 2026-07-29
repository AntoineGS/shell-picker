package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestPreviewMetricsAggregateStartedAndFinishedByInternalTrace(t *testing.T) {
	metrics := &pickerMetrics{traceID: [16]byte{1, 2, 3}}
	if err := metrics.recordPreview(sessionipc.PreviewRequest{Phase: "started", Renderer: "bat"}); err != nil {
		t.Fatal(err)
	}
	if err := metrics.recordPreview(sessionipc.PreviewRequest{Phase: "finished", Renderer: "bat", Outcome: "ok",
		DurationUS: 1250, ChildStarts: 2, MaxLiveChildren: 1}); err != nil {
		t.Fatal(err)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.previewStarted != 1 || metrics.previewFinished != 1 || metrics.previewDuration != 1250*time.Microsecond ||
		metrics.previewChildStarts != 2 || metrics.previewMaxLive != 1 || metrics.rendererStarted["bat"] != 1 ||
		metrics.rendererFinished["bat"] != 1 || metrics.previewOutcomes["ok"] != 1 {
		t.Fatalf("metrics=%+v", metrics)
	}
	if metrics.traceID != [16]byte{1, 2, 3} {
		t.Fatalf("trace ID changed: %x", metrics.traceID)
	}
}

func TestPreviewMetricsRejectInvalidPhaseAndBounds(t *testing.T) {
	tests := []sessionipc.PreviewRequest{
		{Phase: "resolve"},
		{Phase: "started", Renderer: "bat", DurationUS: 1},
		{Phase: "finished", Renderer: "bat", Outcome: "ok", DurationUS: int64(10*time.Second/time.Microsecond) + 1},
		{Phase: "finished", Renderer: "bat", Outcome: "ok", ChildStarts: 4},
		{Phase: "finished", Renderer: "bat", Outcome: "ok", ChildStarts: 1, MaxLiveChildren: 2},
		{Phase: "finished", Renderer: "bat", Outcome: "ok", MaxLiveChildren: 1},
		{Phase: "finished", Renderer: "native", Outcome: "ok", ChildStarts: 1, MaxLiveChildren: 1},
	}
	for _, request := range tests {
		metrics := &pickerMetrics{}
		if err := metrics.recordPreview(request); err == nil {
			t.Errorf("recordPreview(%+v) succeeded", request)
		}
		if metrics.previewStarted != 0 || metrics.previewFinished != 0 {
			t.Errorf("invalid request changed metrics: %+v", metrics)
		}
	}
}

func TestPickerMetricsDoNotExposeTraceOrRequestData(t *testing.T) {
	metrics := &pickerMetrics{traceID: [16]byte{0xff}}
	wire, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != "{}" {
		t.Fatalf("internal metrics serialized as %s", wire)
	}
}

func TestPreviewMetricLabelsAreCardinalityBounded(t *testing.T) {
	metrics := &pickerMetrics{}
	for index := range maxMetricLabels + 2 {
		renderer := string([]byte{'r', byte('A' + index%26), byte('!' + index/26)})
		if err := metrics.recordPreview(sessionipc.PreviewRequest{Phase: "started", Renderer: renderer}); err != nil {
			t.Fatal(err)
		}
	}
	if len(metrics.rendererStarted) != maxMetricLabels || metrics.rendererOverflow != 2 {
		t.Fatalf("labels=%d overflow=%d", len(metrics.rendererStarted), metrics.rendererOverflow)
	}
}
