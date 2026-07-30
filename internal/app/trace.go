package app

import (
	"fmt"
	"io"
	"sync"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type pickerTrace struct {
	trace      *integrationpkg.Trace
	sink       io.WriteCloser
	diagnostic io.Writer
	once       sync.Once
}

func openPickerTrace(path string, sessionID [16]byte, diagnostic io.Writer) (*pickerTrace, error) {
	if path == "" {
		return nil, nil
	}
	sink, err := openTraceSink(path)
	if err != nil {
		return nil, fmt.Errorf("open picker trace: %w", err)
	}
	return &pickerTrace{trace: integrationpkg.NewTrace(sink, sessionID), sink: sink, diagnostic: diagnostic}, nil
}

func (trace *pickerTrace) event(event integrationpkg.TraceEvent) {
	if trace == nil {
		return
	}
	if err := trace.trace.Event(event); err != nil {
		trace.disabledDiagnostic()
	}
}

func (trace *pickerTrace) finish(outcome protocol.Outcome, primary error) (protocol.Outcome, error) {
	if trace == nil {
		return outcome, primary
	}
	status := "error"
	if primary == nil {
		status = string(outcome.Status)
	}
	trace.event(integrationpkg.TraceEvent{Name: "session.close", Outcome: status})
	if trace.sink != nil {
		if err := trace.sink.Close(); err != nil {
			trace.disabledDiagnostic()
		}
	}
	return outcome, primary
}

func (trace *pickerTrace) disabledDiagnostic() {
	trace.once.Do(func() {
		if trace.diagnostic != nil {
			_, _ = fmt.Fprintln(trace.diagnostic, "shell-picker: trace disabled")
		}
	})
}
