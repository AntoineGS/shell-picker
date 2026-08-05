package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

func TestPickerTraceConcurrentEventsAndCloseProduceCompleteRecords(t *testing.T) {
	var output bytes.Buffer
	trace := &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1, 2, 3})}
	if err := trace.event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"}); err != nil {
		t.Fatal(err)
	}

	const workers = 32
	const eventsPerWorker = 64
	var successful atomic.Int64
	start := make(chan struct{})
	var writers sync.WaitGroup
	for range workers {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for range eventsPerWorker {
				err := trace.event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"})
				switch {
				case err == nil:
					successful.Add(1)
				case errors.Is(err, errPickerTraceClosed):
				default:
					t.Errorf("concurrent event error = %v", err)
				}
			}
		}()
	}

	const closers = 8
	var closeWait sync.WaitGroup
	closeResults := make(chan error, closers)
	for range closers {
		closeWait.Add(1)
		go func() {
			defer closeWait.Done()
			<-start
			closeResults <- trace.close()
		}()
	}
	close(start)
	writers.Wait()
	closeWait.Wait()
	close(closeResults)
	for err := range closeResults {
		if err != nil {
			t.Errorf("close error = %v", err)
		}
	}
	if err := trace.event(integrationpkg.TraceEvent{Name: "session.start", Outcome: "cp"}); !errors.Is(err, errPickerTraceClosed) {
		t.Fatalf("write after close error = %v, want errPickerTraceClosed", err)
	}
	if err := trace.close(); err != nil {
		t.Fatalf("repeated close error = %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != int(successful.Load())+1 {
		t.Fatalf("JSONL record count = %d, want %d; output=%q", len(lines), successful.Load()+1, output.String())
	}
	for index, line := range lines {
		if line == "" {
			t.Fatalf("JSONL line %d is blank", index)
		}
		var record integrationpkg.TraceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("JSONL line %d is not one complete record: %v; line=%q", index, err, line)
		}
		if err := integrationpkg.ValidateTraceRecordAt(record, time.Time{}); err != nil {
			t.Fatalf("JSONL line %d failed trace validation: %v", index, err)
		}
	}
}
