package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

func TestPickerBackendTracesOnlyAcceptedPreviewFinishedTelemetryInOrder(t *testing.T) {
	var output bytes.Buffer
	backend := &pickerBackend{
		metrics: &pickerMetrics{},
		trace:   &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1})},
	}
	const currentCanary = "VE9LRU4tUVVFUlktQ0FOQVJZ"
	started := sessionipc.PreviewRequest{Phase: "started", CurrentItemBase64: currentCanary, Renderer: "eza"}
	if err := backend.RecordPreview(context.Background(), started); err != nil {
		t.Fatal(err)
	}
	invalid := sessionipc.PreviewRequest{Phase: "finished", CurrentItemBase64: currentCanary, Renderer: "eza", Outcome: "ok", ChildStarts: 4}
	if err := backend.RecordPreview(context.Background(), invalid); err == nil {
		t.Fatal("invalid finished telemetry was accepted")
	}
	finished := sessionipc.PreviewRequest{Phase: "finished", CurrentItemBase64: currentCanary, Renderer: "eza", Outcome: "error", ChildStarts: 1, MaxLiveChildren: 1}
	if err := backend.RecordPreview(context.Background(), finished); err != nil {
		t.Fatal(err)
	}

	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("trace lines=%d want 2: %s", len(lines), output.Bytes())
	}
	var records []integrationpkg.TraceRecord
	for _, line := range lines {
		var record integrationpkg.TraceRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if records[0].Event != "preview.dispatch" || records[0].Renderer != "eza" || records[0].Outcome != "ok" ||
		records[1].Event != "preview.finished" || records[1].Renderer != "eza" || records[1].Outcome != "error" {
		t.Fatalf("records=%+v", records)
	}
	text := output.String()
	for _, forbidden := range []string{"token", "query", "current_item", currentCanary, strings.Repeat("x", 65)} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("trace leaked %q", forbidden)
		}
	}
}
