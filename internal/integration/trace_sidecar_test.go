package integration

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestTraceAcceptsSecretFreeSidecarDiagnostics(t *testing.T) {
	var output bytes.Buffer
	trace := NewTrace(&output, fixedSessionID())
	valid := []TraceEvent{
		{Name: "sidecar.get", Outcome: "success", SidecarAttempt: 1, LocalDuration: 2 * time.Millisecond},
		{Name: "sidecar.get", Outcome: "transient", SidecarAttempt: 2, LocalDuration: 3 * time.Millisecond},
		{Name: "sidecar.post", Outcome: "terminal", SidecarAttempt: 1, LocalDuration: 4 * time.Millisecond},
		{Name: "sidecar.stop", Outcome: "terminal"},
	}
	for _, event := range valid {
		if err := trace.Event(event); err != nil {
			t.Fatalf("valid sidecar event %+v: %v", event, err)
		}
	}
	for _, event := range []TraceEvent{
		{Name: "sidecar.get", Outcome: "unknown", SidecarAttempt: 1},
		{Name: "sidecar.get", Outcome: "success", SidecarAttempt: 0},
		{Name: "sidecar.get", Outcome: "success", SidecarAttempt: 1, Generation: 1},
		{Name: "sidecar.get", Outcome: "success", SidecarAttempt: 1, Path: []byte("secret")},
	} {
		if err := trace.Event(event); err == nil {
			t.Fatalf("invalid sidecar event accepted: %+v", event)
		}
	}
	text := output.String()
	for _, forbidden := range []string{"http://", "api-key", "selected", "body", "state"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sidecar trace leaked %q: %s", forbidden, text)
		}
	}
}
