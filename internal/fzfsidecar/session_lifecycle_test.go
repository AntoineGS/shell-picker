package fzfsidecar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestStopAndWaitCloseTransportExactlyOnce(t *testing.T) {
	observedTicker := newObservingTicker()
	transport := newGatedTransport()
	session, err := New(protocol.PickerCD, WithTimer(func(time.Duration) timer { return observedTicker }), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	waitForTestSignal(t, transport.started, "blocked transport start")
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	session.Stop()
	if got := transport.idleCalls.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", got)
	}
	if got := observedTicker.stopCalls.Load(); got != 1 {
		t.Fatalf("ticker Stop calls = %d, want 1", got)
	}
}

func TestConcurrentRepeatedLifecycleCallsAreIdempotent(t *testing.T) {
	manual := newManualTicker()
	transport := newGatedTransport()
	session, err := New(protocol.PickerCD, WithTimer(func(time.Duration) timer { return manual }), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	waitForTestSignal(t, transport.started, "repeated lifecycle transport start")
	var group sync.WaitGroup
	for range 20 {
		group.Add(1)
		go func() {
			defer group.Done()
			session.Start(context.Background())
			session.Stop()
			if err := session.Wait(); err != nil {
				t.Errorf("Wait: %v", err)
			}
		}()
	}
	group.Wait()
	if err := session.Wait(); err != nil {
		t.Fatalf("final Wait: %v", err)
	}
	requestCount := transport.calls.Load()
	manual.Tick(time.Now())
	manual.Tick(time.Now())
	if got := transport.calls.Load(); got != requestCount {
		t.Fatalf("requests after Wait = %d, want %d", got, requestCount)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want 1", requestCount)
	}
	if got := manual.stopCalls.Load(); got != 1 {
		t.Fatalf("timer Stop calls = %d, want 1", got)
	}
	if got := transport.idleCalls.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", got)
	}
}

func TestSessionStopAndWaitAreSafeBeforeStartAndRepeated(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected request")
	})
	session, err := New(protocol.PickerCD, WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("pre-start Wait: %v", err)
	}
	session.Stop()
	session.Start(context.Background())
	session.Start(context.Background())
	if err := session.Wait(); err != nil {
		t.Fatalf("repeated Wait: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("requests after pre-start Stop = %d, want 0", got)
	}
}

func TestConcurrentPreStartLifecycleCallsReachOneTerminalState(t *testing.T) {
	manual := newManualTicker()
	transport := newGatedTransport()
	session, err := New(protocol.PickerCD, WithTimer(func(time.Duration) timer { return manual }), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < 60; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			select {
			case <-start:
			case <-time.After(time.Second):
				t.Errorf("timed out waiting for lifecycle start barrier")
				return
			}
			switch index % 3 {
			case 0:
				session.Start(context.Background())
			case 1:
				session.Stop()
			default:
				if err := session.Wait(); err != nil {
					t.Errorf("Wait: %v", err)
				}
			}
		}(index)
	}
	close(start)
	group.Wait()

	if err := session.Wait(); err != nil {
		t.Fatalf("final Wait: %v", err)
	}
	requestCount := transport.calls.Load()
	if requestCount > 1 {
		t.Fatalf("request count = %d, want at most 1", requestCount)
	}
	if requestCount == 1 && manual.stopCalls.Load() != 1 {
		t.Fatalf("timer Stop calls = %d, want 1 after a started session", manual.stopCalls.Load())
	}
	if requestCount == 0 && manual.stopCalls.Load() != 0 {
		t.Fatalf("timer Stop calls = %d, want 0 when Start lost", manual.stopCalls.Load())
	}

	session.Start(context.Background())
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("terminal Wait: %v", err)
	}
	if requestCount > 0 {
		manual.Tick(time.Time{})
	}
	if got := transport.calls.Load(); got != requestCount {
		t.Fatalf("requests after terminal Wait = %d, want %d", got, requestCount)
	}
	if got := transport.idleCalls.Load(); got != 1 {
		t.Fatalf("CloseIdleConnections calls = %d, want 1", got)
	}
}

func TestWaitReturnsProgrammingErrorsButNotPollFailures(t *testing.T) {
	programming, err := New(protocol.PickerCD, WithTimer(func(time.Duration) timer { return nil }))
	if err != nil {
		t.Fatalf("New programming case: %v", err)
	}
	programming.Start(context.Background())
	if err := programming.Wait(); err == nil {
		t.Fatal("Wait returned nil for a nil ticker programming error")
	}

	pollFailure, err := New(protocol.PickerCD, WithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, "text/plain", "no"), nil
	})))
	if err != nil {
		t.Fatalf("New poll failure case: %v", err)
	}
	pollFailure.Start(context.Background())
	if err := pollFailure.Wait(); err != nil {
		t.Fatalf("Wait returned poll failure: %v", err)
	}
}

func TestDialerOptionIsUsedByTheDedicatedTransport(t *testing.T) {
	var dials atomic.Int32
	dialer := func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("dial blocked for test")
	}
	session, err := New(protocol.PickerCD, WithDialer(dialer), WithReadinessTimeout(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dial calls = %d, want 1", got)
	}
}

func TestSessionCancellationUnblocksReadinessAndBlockedRequests(t *testing.T) {
	tests := []struct {
		name          string
		makeTransport func(started chan<- struct{}) http.RoundTripper
	}{
		{name: "readiness wait", makeTransport: func(started chan<- struct{}) http.RoundTripper {
			return roundTripFunc(func(*http.Request) (*http.Response, error) {
				onceSignal(started)
				return nil, fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED)
			})
		}},
		{name: "GET request", makeTransport: func(started chan<- struct{}) http.RoundTripper {
			return roundTripFunc(func(request *http.Request) (*http.Response, error) {
				onceSignal(started)
				<-request.Context().Done()
				return nil, request.Context().Err()
			})
		}},
		{name: "POST request", makeTransport: func(started chan<- struct{}) http.RoundTripper {
			var call int
			return roundTripFunc(func(request *http.Request) (*http.Response, error) {
				call++
				if call == 1 {
					return response(http.StatusOK, "application/json", `{"totalCount":1,"matchCount":1,"selected":[]}`), nil
				}
				onceSignal(started)
				<-request.Context().Done()
				return nil, request.Context().Err()
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			manual := newManualTicker()
			session, err := New(protocol.PickerCD, WithTimer(func(time.Duration) timer { return manual }), WithTransport(test.makeTransport(started)))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			session.Start(context.Background())
			waitForTestSignal(t, started, "cancellation test request start")
			select {
			case <-session.done:
				t.Fatal("session finished before cancellation")
			default:
			}
			session.Stop()
			if err := session.Wait(); err != nil {
				t.Fatalf("Wait: %v", err)
			}
		})
	}
}

func TestSessionObserverSuppressesCanceledOperationEvents(t *testing.T) {
	tests := []struct {
		name          string
		method        string
		cancelMode    string
		wantKinds     []ObserverEventKind
		wantStop      ObserverStopReason
		wantGETBefore bool
	}{
		{
			name:       "parent cancel during GET",
			method:     http.MethodGet,
			cancelMode: "cancel",
			wantKinds:  []ObserverEventKind{ObserverStop},
			wantStop:   ObserverStopContextCanceled,
		},
		{
			name:       "parent deadline during GET",
			method:     http.MethodGet,
			cancelMode: "deadline",
			wantKinds:  []ObserverEventKind{ObserverStop},
			wantStop:   ObserverStopContextCanceled,
		},
		{
			name:          "parent cancel during POST",
			method:        http.MethodPost,
			cancelMode:    "cancel",
			wantKinds:     []ObserverEventKind{ObserverGetSuccess, ObserverStop},
			wantStop:      ObserverStopContextCanceled,
			wantGETBefore: true,
		},
		{
			name:          "parent deadline during POST",
			method:        http.MethodPost,
			cancelMode:    "deadline",
			wantKinds:     []ObserverEventKind{ObserverGetSuccess, ObserverStop},
			wantStop:      ObserverStopContextCanceled,
			wantGETBefore: true,
		},
		{
			name:       "session stop during GET",
			method:     http.MethodGet,
			cancelMode: "session-stop",
			wantKinds:  []ObserverEventKind{ObserverStop},
			wantStop:   ObserverStopRequested,
		},
		{
			name:          "session stop during POST",
			method:        http.MethodPost,
			cancelMode:    "session-stop",
			wantKinds:     []ObserverEventKind{ObserverGetSuccess, ObserverStop},
			wantStop:      ObserverStopRequested,
			wantGETBefore: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			observer := &recordingObserver{}
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if test.wantGETBefore && request.Method == http.MethodGet {
					return response(http.StatusOK, "application/json", `{"totalCount":1,"matchCount":1,"selected":[]}`), nil
				}
				if test.wantGETBefore && request.Method != http.MethodPost {
					t.Fatalf("request method=%q, want POST", request.Method)
				}
				if !test.wantGETBefore && request.Method != test.method {
					t.Fatalf("request method=%q, want %s", request.Method, test.method)
				}
				onceSignal(started)
				<-request.Context().Done()
				return nil, request.Context().Err()
			})

			parent := context.Background()
			cancel := func() {}
			switch test.cancelMode {
			case "cancel":
				parent, cancel = context.WithCancel(parent)
			case "deadline":
				parent, cancel = context.WithDeadline(parent, time.Now().Add(100*time.Millisecond))
			}
			defer cancel()

			session, err := New(protocol.PickerCD,
				WithTransport(transport),
				WithObserver(observer),
				WithInterval(time.Millisecond),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			session.Start(parent)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for blocked sidecar request")
			}
			switch test.cancelMode {
			case "cancel":
				cancel()
			case "deadline":
				select {
				case <-parent.Done():
				case <-time.After(time.Second):
					t.Fatal("parent deadline did not expire")
				}
			case "session-stop":
				session.Stop()
			}
			if err := session.Wait(); err != nil {
				t.Fatalf("Wait: %v", err)
			}

			events := observer.Events()
			if len(events) != len(test.wantKinds) {
				t.Fatalf("observer sequence=%+v, want kinds=%v", events, test.wantKinds)
			}
			for index, wantKind := range test.wantKinds {
				if events[index].Kind != wantKind {
					t.Fatalf("observer event %d=%+v, want kind %d", index, events[index], wantKind)
				}
			}
			stop := events[len(events)-1]
			if stop.StopReason != test.wantStop {
				t.Fatalf("observer stop=%+v, want reason %d", stop, test.wantStop)
			}
		})
	}
}

func TestSessionStopReasonPreservesTheFirstCancellationCause(t *testing.T) {
	for _, test := range []struct {
		name        string
		parentFirst bool
		want        ObserverStopReason
	}{
		{name: "parent cancellation first", parentFirst: true, want: ObserverStopContextCanceled},
		{name: "session stop first", parentFirst: false, want: ObserverStopRequested},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			observer := &recordingObserver{}
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				close(started)
				<-request.Context().Done()
				return nil, request.Context().Err()
			})
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			session, err := New(protocol.PickerCD, WithTransport(transport), WithObserver(observer))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			session.Start(parent)
			waitForTestSignal(t, started, "blocked request")
			if test.parentFirst {
				cancel()
				session.Stop()
			} else {
				session.Stop()
				cancel()
			}
			if err := session.Wait(); err != nil {
				t.Fatalf("Wait: %v", err)
			}
			events := observer.Events()
			if len(events) != 1 {
				t.Fatalf("observer events=%+v, want one stop event", events)
			}
			if events[0].Kind != ObserverStop || events[0].StopReason != test.want {
				t.Fatalf("observer event=%+v, want stop reason %d", events[0], test.want)
			}
		})
	}
}

func TestSessionStopClosesAnActiveResponseBody(t *testing.T) {
	started := make(chan struct{})
	body := &blockingBody{started: started, closed: make(chan struct{})}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
	})
	session, err := New(protocol.PickerCD, WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	<-started
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestParentCancellationClosesAnActiveResponseBody(t *testing.T) {
	started := make(chan struct{})
	body := &blockingBody{started: started, closed: make(chan struct{})}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: body}, nil
	})
	session, err := New(protocol.PickerCD, WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parent, cancel := context.WithCancel(context.Background())
	session.Start(parent)
	<-started
	cancel()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
