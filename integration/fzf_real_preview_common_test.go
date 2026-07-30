package integration

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

func TestValidateFinishedTraceRejectsDelayedErrorsAndExtraFinishes(t *testing.T) {
	for _, test := range []struct {
		name   string
		events []traceEvent
		want   int
		fail   bool
	}{
		{name: "none expected", want: 0},
		{name: "one expected", want: 1, events: []traceEvent{{Event: "preview.finished", Renderer: "eza", Outcome: "ok"}}},
		{name: "delayed killed error", want: 1, fail: true, events: []traceEvent{
			{Event: "preview.finished", Renderer: "eza", Outcome: "ok"},
			{Event: "preview.finished", Renderer: "eza", Outcome: "error"},
		}},
		{name: "wrong outcome", want: 1, fail: true, events: []traceEvent{{Event: "preview.finished", Renderer: "eza", Outcome: "error"}}},
		{name: "wrong renderer", want: 1, fail: true, events: []traceEvent{{Event: "preview.finished", Renderer: "chafa", Outcome: "ok"}}},
		{name: "unexpected delayed finish", want: 0, fail: true, events: []traceEvent{{Event: "preview.finished", Renderer: "eza", Outcome: "error"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateFinishedTrace(test.events, "eza", test.want)
			if (err != nil) != test.fail {
				t.Fatalf("error=%v fail=%v", err, test.fail)
			}
		})
	}
}

func assertPreviewTraceCount(t *testing.T, events []traceEvent, name, renderer, outcome string, want int) {
	t.Helper()
	got := 0
	for _, event := range events {
		if event.Event == name && event.Renderer == renderer && (outcome == "" || event.Outcome == outcome) {
			got++
		}
	}
	if got != want {
		t.Fatalf("%s renderer=%s outcome=%s count=%d want %d; events=%+v", name, renderer, outcome, got, want, events)
	}
}

func validateFinishedTrace(events []traceEvent, renderer string, want int) error {
	finished := make([]traceEvent, 0, 1)
	for _, event := range events {
		if event.Event == "preview.finished" {
			finished = append(finished, event)
		}
	}
	if len(finished) != want {
		return fmt.Errorf("preview.finished total=%d want %d", len(finished), want)
	}
	if want == 1 && (finished[0].Renderer != renderer || finished[0].Outcome != "ok") {
		return fmt.Errorf("preview.finished renderer/outcome=%s/%s want %s/ok", finished[0].Renderer, finished[0].Outcome, renderer)
	}
	return nil
}

func assertFinishedTrace(t *testing.T, events []traceEvent, renderer string, want int) {
	t.Helper()
	if err := validateFinishedTrace(events, renderer, want); err != nil {
		t.Fatalf("%v; events=%+v", err, events)
	}
}

type controlEvent struct {
	Event   string `json:"event"`
	Nonce   string `json:"nonce,omitempty"`
	PID     int    `json:"pid"`
	Columns int    `json:"columns,omitempty"`
	Lines   int    `json:"lines,omitempty"`
}

func readControlFrame(reader io.Reader, destination *controlEvent) error {
	var size uint32
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > 4096 {
		return io.ErrUnexpectedEOF
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, destination)
}

func writeControlFrame(writer io.Writer, event controlEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if err := binary.Write(writer, binary.BigEndian, uint32(len(payload))); err != nil {
		return err
	}
	_, err = writer.Write(payload)
	return err
}
