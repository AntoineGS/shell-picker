package integration

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"testing"
)

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
