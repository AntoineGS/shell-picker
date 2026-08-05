package fzfsidecar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSessionObserverIsDefaultDisabled(t *testing.T) {
	manual := newControlledTimer()
	requests := make(chan string, 2)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests <- request.Method
		if request.Method == http.MethodGet {
			return response(http.StatusOK, "application/json", `{"totalCount":1,"matchCount":1,"selected":[]}`), nil
		}
		return response(http.StatusOK, "text/plain", "ok"), nil
	})
	session, err := New(protocol.PickerCD, WithTimer(func(time.Duration) timer { return manual }), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, requests, "observer GET"); got != http.MethodGet {
		t.Fatalf("first request=%q, want GET", got)
	}
	if got := receiveTestValue(t, requests, "observer POST"); got != http.MethodPost {
		t.Fatalf("second request=%q, want POST", got)
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestWithObserverRejectsNil(t *testing.T) {
	if _, err := New(protocol.PickerCD, WithObserver(nil)); err == nil {
		t.Fatal("WithObserver(nil) was accepted")
	}
}

func TestSessionObserverEmitsClosedEventsWithCountersAndDurations(t *testing.T) {
	manual := newControlledTimer()
	observer := &recordingObserver{}
	requests := make(chan string, 2)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests <- request.Method
		if request.Method == http.MethodGet {
			return response(http.StatusOK, "application/json", `{"totalCount":1,"matchCount":1,"selected":[]}`), nil
		}
		return response(http.StatusOK, "text/plain", "ok"), nil
	})
	session, err := New(protocol.PickerCD,
		WithTimer(func(time.Duration) timer { return manual }),
		WithTransport(transport),
		WithObserver(observer),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	receiveTestValue(t, requests, "observer event GET")
	receiveTestValue(t, requests, "observer event POST")
	observer.WaitForCount(t, 2)
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	events := observer.Events()
	if len(events) != 3 {
		t.Fatalf("observer events=%+v, want GET success, POST success, stop", events)
	}
	if events[0].Kind != ObserverGetSuccess || events[0].Attempt != 1 {
		t.Fatalf("GET observer event=%+v, want success attempt 1", events[0])
	}
	if events[0].Method != ObserverMethodGet || events[0].Phase != ObserverPhaseReadiness ||
		events[0].Status != ObserverStatusSuccess || events[0].Reason != ObserverReasonHTTPStatus ||
		events[0].HTTPStatus != http.StatusOK {
		t.Fatalf("GET observer metadata=%+v, want GET/readiness/success/HTTP 200", events[0])
	}
	if events[1].Kind != ObserverPostSuccess || events[1].Attempt != 1 {
		t.Fatalf("POST observer event=%+v, want success attempt 1", events[1])
	}
	if events[1].Method != ObserverMethodPost || events[1].Phase != ObserverPhaseReady ||
		events[1].Status != ObserverStatusSuccess || events[1].Reason != ObserverReasonHTTPStatus ||
		events[1].HTTPStatus != http.StatusOK {
		t.Fatalf("POST observer metadata=%+v, want POST/ready/success/HTTP 200", events[1])
	}
	if events[2].Kind != ObserverStop || events[2].StopReason == 0 {
		t.Fatalf("stop observer event=%+v, want closed stop reason", events[2])
	}
	for _, event := range events[:2] {
		if event.Duration < 0 {
			t.Fatalf("observer duration=%v, want non-negative", event.Duration)
		}
		if event.StopReason != 0 {
			t.Fatalf("operation observer event carries stop reason: %+v", event)
		}
	}
}

func TestSessionObserverIncludesSafeFailureMetadata(t *testing.T) {
	tests := []struct {
		name       string
		getStatus  int
		getBody    string
		getType    string
		postStatus int
		postError  bool
		wantMethod ObserverMethod
		wantStatus ObserverStatus
		wantReason ObserverReason
		wantCode   int
	}{
		{
			name:       "post service unavailable",
			getStatus:  http.StatusOK,
			getBody:    `{"totalCount":1,"matchCount":1,"selected":[]}`,
			getType:    "application/json",
			postStatus: http.StatusServiceUnavailable,
			wantMethod: ObserverMethodPost,
			wantStatus: ObserverStatusTransient,
			wantReason: ObserverReasonHTTPStatus,
			wantCode:   http.StatusServiceUnavailable,
		},
		{
			name:       "post unauthorized",
			getStatus:  http.StatusOK,
			getBody:    `{"totalCount":1,"matchCount":1,"selected":[]}`,
			getType:    "application/json",
			postStatus: http.StatusUnauthorized,
			wantMethod: ObserverMethodPost,
			wantStatus: ObserverStatusTerminal,
			wantReason: ObserverReasonHTTPStatus,
			wantCode:   http.StatusUnauthorized,
		},
		{
			name:       "malformed get",
			getStatus:  http.StatusOK,
			getBody:    "not-json",
			getType:    "application/json",
			postStatus: 0,
			wantMethod: ObserverMethodGet,
			wantStatus: ObserverStatusTerminal,
			wantReason: ObserverReasonInvalidJSON,
			wantCode:   http.StatusOK,
		},
		{
			name:       "invalid get content type",
			getStatus:  http.StatusOK,
			getBody:    "not-json",
			getType:    "text/plain",
			wantMethod: ObserverMethodGet,
			wantStatus: ObserverStatusTerminal,
			wantReason: ObserverReasonInvalidMIME,
			wantCode:   http.StatusOK,
		},
		{
			name:       "post transport failure",
			getStatus:  http.StatusOK,
			getBody:    `{"totalCount":1,"matchCount":1,"selected":[]}`,
			getType:    "application/json",
			postError:  true,
			wantMethod: ObserverMethodPost,
			wantStatus: ObserverStatusTerminal,
			wantReason: ObserverReasonTransport,
			wantCode:   0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := &recordingObserver{}
			started := make(chan struct{}, 1)
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				onceSignal(started)
				if request.Method == http.MethodGet {
					return response(test.getStatus, test.getType, test.getBody), nil
				}
				if test.postError {
					return nil, errors.New("transport failure with secret=must-not-escape")
				}
				return response(test.postStatus, "text/plain", "secret body must not escape"), nil
			})
			session, err := New(protocol.PickerCD, WithTransport(transport), WithObserver(observer), WithInterval(time.Millisecond))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			session.Start(context.Background())
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for sidecar request")
			}
			observer.WaitForMethod(t, test.wantMethod)
			session.Stop()
			if err := session.Wait(); err != nil {
				t.Fatalf("Wait: %v", err)
			}

			var got ObserverEvent
			for _, event := range observer.Events() {
				if event.Method == test.wantMethod {
					got = event
					break
				}
			}
			if got.Method != test.wantMethod || got.Status != test.wantStatus || got.Reason != test.wantReason || got.HTTPStatus != test.wantCode {
				t.Fatalf("observer failure metadata=%+v, want method=%d status=%d reason=%d code=%d", got, test.wantMethod, test.wantStatus, test.wantReason, test.wantCode)
			}
			if got.Duration < 0 || got.Duration > requestTimeout {
				t.Fatalf("observer duration=%v, want bounded non-negative duration", got.Duration)
			}
			if strings.Contains(fmt.Sprintf("%+v", got), "secret") {
				t.Fatalf("observer event contains secret-bearing data: %+v", got)
			}
		})
	}
}

type recordingObserver struct {
	mu      sync.Mutex
	events  []ObserverEvent
	changed chan struct{}
}

func (observer *recordingObserver) Observe(event ObserverEvent) {
	observer.mu.Lock()
	if observer.changed == nil {
		observer.changed = make(chan struct{})
	}
	observer.events = append(observer.events, event)
	close(observer.changed)
	observer.changed = make(chan struct{})
	observer.mu.Unlock()
}

func (observer *recordingObserver) Events() []ObserverEvent {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return append([]ObserverEvent(nil), observer.events...)
}

func (observer *recordingObserver) WaitForCount(t *testing.T, want int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		observer.mu.Lock()
		if len(observer.events) >= want {
			observer.mu.Unlock()
			return
		}
		changed := observer.changed
		observer.mu.Unlock()
		if changed == nil {
			select {
			case <-time.After(time.Millisecond):
				continue
			case <-deadline.C:
				t.Fatalf("observer has not emitted an event")
			}
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("observer events=%+v, want at least %d", observer.Events(), want)
		}
	}
}

func (observer *recordingObserver) WaitForMethod(t *testing.T, want ObserverMethod) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		observer.mu.Lock()
		for _, event := range observer.events {
			if event.Method == want {
				observer.mu.Unlock()
				return
			}
		}
		changed := observer.changed
		observer.mu.Unlock()
		if changed == nil {
			select {
			case <-time.After(time.Millisecond):
				continue
			case <-deadline.C:
				t.Fatalf("observer has no method %d: %+v", want, observer.Events())
			}
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("observer has no method %d: %+v", want, observer.Events())
		}
	}
}
