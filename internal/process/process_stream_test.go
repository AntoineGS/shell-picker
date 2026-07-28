package process

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
)

func TestRejectsNonIdentifiableValueCloserBeforeAttempt(t *testing.T) {
	state := &trickyCloserState{blocked: make(chan struct{}), closed: make(chan struct{})}
	var typedNil *blockingStream
	for name, stream := range map[string]io.Writer{
		"value":     trickyCloser{state: state, payload: []int{1, 2, 3}},
		"typed-nil": typedNil,
	} {
		t.Run(name, func(t *testing.T) {
			var events []ProcessEvent
			spec := helperSpec("exit", "0")
			spec.Stdout = stream
			_, err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}).Start(context.Background(), spec)
			if !errors.Is(err, ErrInvalidStream) || len(events) != 0 {
				t.Fatalf("err=%v events=%+v", err, events)
			}
		})
	}
}

func TestRejectsTypedNilFilesBeforeAttempt(t *testing.T) {
	var file *os.File
	for _, test := range []struct {
		name string
		set  func(*Spec)
	}{
		{"stdin", func(spec *Spec) { spec.Stdin = file }},
		{"stdout", func(spec *Spec) { spec.Stdout = file }},
		{"stderr", func(spec *Spec) { spec.Stderr = file }},
	} {
		t.Run(test.name, func(t *testing.T) {
			marker := t.TempDir() + string(os.PathSeparator) + "started"
			spec := helperSpec("mark-start", marker)
			test.set(&spec)
			var events []ProcessEvent
			err := (Runner{Observe: func(event ProcessEvent) { events = append(events, event) }}).Run(context.Background(), spec)
			if !errors.Is(err, ErrInvalidStream) || len(events) != 0 {
				t.Fatalf("err=%v events=%+v", err, events)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("process started: %v", err)
			}
		})
	}
}
