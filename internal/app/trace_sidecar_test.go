package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSidecarObserverWritesOnlySchemaValidDiagnostics(t *testing.T) {
	var output bytes.Buffer
	trace := &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1})}
	observer := newSidecarTraceObserver(trace)
	observer.Observe(fzfsidecar.ObserverEvent{Kind: fzfsidecar.ObserverGetSuccess, Attempt: 4, Duration: 3 * time.Millisecond})
	observer.Observe(fzfsidecar.ObserverEvent{Kind: fzfsidecar.ObserverPostTransient, Attempt: 7, Duration: 5 * time.Millisecond})
	observer.Observe(fzfsidecar.ObserverEvent{Kind: fzfsidecar.ObserverStop, StopReason: fzfsidecar.ObserverStopTransientWindow})

	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var record integrationpkg.TraceRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("trace record %q: %v", line, err)
		}
		if err := integrationpkg.ValidateTraceRecordAt(record, time.Time{}); err != nil {
			t.Fatalf("invalid sidecar trace record %+v: %v", record, err)
		}
		if record.Event != "sidecar.get" && record.Event != "sidecar.post" && record.Event != "sidecar.stop" {
			t.Fatalf("unexpected event=%q", record.Event)
		}
		if strings.Contains(line, "http") || strings.Contains(line, "key") || strings.Contains(line, "state") || strings.Contains(line, "body") {
			t.Fatalf("sidecar trace contains secret/state vocabulary: %q", line)
		}
	}
}

func TestSidecarTraceObserverSuppressesCanceledOperationRecords(t *testing.T) {
	for _, test := range []struct {
		name      string
		post      bool
		wantEvent []struct{ name, outcome string }
	}{
		{
			name: "blocked GET",
			wantEvent: []struct{ name, outcome string }{
				{name: "sidecar.stop", outcome: "context-canceled"},
			},
		},
		{
			name: "blocked POST",
			post: true,
			wantEvent: []struct{ name, outcome string }{
				{name: "sidecar.get", outcome: "success"},
				{name: "sidecar.stop", outcome: "context-canceled"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			transport := sidecarTraceRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if test.post && request.Method == http.MethodGet {
					return sidecarTraceResponse(http.StatusOK, "application/json", `{"totalCount":1,"matchCount":1,"selected":[]}`), nil
				}
				onceSidecarTraceSignal(started)
				<-request.Context().Done()
				return nil, request.Context().Err()
			})
			var output bytes.Buffer
			trace := &pickerTrace{trace: integrationpkg.NewTrace(&output, [16]byte{1})}
			session, err := fzfsidecar.New(protocol.PickerCD,
				fzfsidecar.WithTransport(transport),
				fzfsidecar.WithInterval(time.Millisecond),
				fzfsidecar.WithObserver(newSidecarTraceObserver(trace)),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			parent, cancel := context.WithCancel(context.Background())
			session.Start(parent)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for blocked sidecar request")
			}
			cancel()
			if err := session.Wait(); err != nil {
				t.Fatalf("Wait: %v", err)
			}

			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if len(lines) != len(test.wantEvent) {
				t.Fatalf("trace lines=%q, want %d records", lines, len(test.wantEvent))
			}
			for index, want := range test.wantEvent {
				var record integrationpkg.TraceRecord
				if err := json.Unmarshal([]byte(lines[index]), &record); err != nil {
					t.Fatalf("trace record %q: %v", lines[index], err)
				}
				if err := integrationpkg.ValidateTraceRecordAt(record, time.Time{}); err != nil {
					t.Fatalf("invalid trace record %+v: %v", record, err)
				}
				if record.Event != want.name || record.Outcome != want.outcome {
					t.Fatalf("trace record %d=%+v, want %s/%s", index, record, want.name, want.outcome)
				}
				if record.Generation != 0 || record.CandidateCount != 0 || record.Renderer != "" || record.Path != "" {
					t.Fatalf("trace record %d carried picker fields: %+v", index, record)
				}
			}
		})
	}
}

type sidecarTraceRoundTripFunc func(*http.Request) (*http.Response, error)

func (function sidecarTraceRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func sidecarTraceResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func onceSidecarTraceSignal(channel chan<- struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}
