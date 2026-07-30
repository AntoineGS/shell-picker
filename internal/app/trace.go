package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

type pickerTrace struct {
	trace      *integrationpkg.Trace
	file       *os.File
	diagnostic io.Writer
	once       sync.Once
}

func openPickerTrace(path string, sessionID [16]byte, diagnostic io.Writer) (*pickerTrace, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open picker trace: %w", err)
	}
	if err := secureTraceFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure picker trace: %w", err)
	}
	return &pickerTrace{trace: integrationpkg.NewTrace(file, sessionID), file: file, diagnostic: diagnostic}, nil
}

func (trace *pickerTrace) event(event integrationpkg.TraceEvent) {
	if trace == nil {
		return
	}
	if err := trace.trace.Event(event); err != nil {
		trace.once.Do(func() {
			if trace.diagnostic != nil {
				_, _ = fmt.Fprintln(trace.diagnostic, "shell-picker: trace disabled")
			}
		})
	}
}

func (trace *pickerTrace) close() error {
	if trace == nil || trace.file == nil {
		return nil
	}
	if err := trace.file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("close picker trace: %w", err)
	}
	return nil
}
