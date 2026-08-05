package fzfsidecar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSessionRetriesReadinessTransportFailuresUntilAValidState(t *testing.T) {
	clock := newManualClock(time.Unix(100, 0))
	manual := newManualTicker()
	var calls atomic.Int32
	observed := make(chan int, 8)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		observed <- int(call)
		switch call {
		case 1, 2:
			return nil, syscall.ECONNREFUSED
		case 3:
			return response(http.StatusOK, "application/json", `{"totalCount":10,"matchCount":3,"selected":[]}`), nil
		case 4:
			return response(http.StatusOK, "text/plain", "ok"), nil
		default:
			return nil, errors.New("unexpected request")
		}
	})
	session, err := New(protocol.PickerCD, WithClock(clock.Now), WithTimer(func(time.Duration) timer { return manual }), WithInterval(time.Second), WithReadinessTimeout(3*time.Second), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, observed, "readiness GET 1"); got != 1 {
		t.Fatalf("first request number = %d, want 1", got)
	}
	clock.Advance(time.Second)
	manual.Tick(time.Now())
	if got := receiveTestValue(t, observed, "readiness GET 2"); got != 2 {
		t.Fatalf("second request number = %d, want 2", got)
	}
	clock.Advance(time.Second)
	manual.Tick(time.Now())
	if got := receiveTestValue(t, observed, "readiness GET 3"); got != 3 {
		t.Fatalf("third request number = %d, want 3", got)
	}
	if got := receiveTestValue(t, observed, "readiness POST"); got != 4 {
		t.Fatalf("fourth request number = %d, want 4", got)
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("request count = %d, want 4", got)
	}
}

func TestSessionRetriesReadinessRequestTimeoutsUntilValidState(t *testing.T) {
	clock := newManualClock(time.Unix(600, 0))
	manual := newControlledTimer()
	var gets atomic.Int32
	getObserved := make(chan int32, 8)
	postObserved := make(chan struct{}, 1)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			postObserved <- struct{}{}
			return response(http.StatusOK, "text/plain", "ok"), nil
		}
		call := gets.Add(1)
		getObserved <- call
		if call <= 2 {
			return nil, context.DeadlineExceeded
		}
		return response(http.StatusOK, "application/json", `{"totalCount":10,"matchCount":3,"selected":[]}`), nil
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now),
		WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second), WithReadinessTimeout(10*time.Second), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, getObserved, "initial timeout GET"); got != 1 {
		t.Fatalf("timeout GET number = %d, want 1", got)
	}
	waitForReadinessReset(t, manual, session.done, "initial timeout retry")
	clock.Advance(time.Second)
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "second timeout GET"); got != 2 {
		t.Fatalf("second timeout GET number = %d, want 2", got)
	}
	waitForReadinessReset(t, manual, session.done, "second timeout retry")
	clock.Advance(time.Second)
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "valid GET after timeout"); got != 3 {
		t.Fatalf("valid GET number = %d, want 3", got)
	}
	waitForTestSignal(t, postObserved, "POST after timeout recovery")
	if got := gets.Load(); got != 3 {
		t.Fatalf("GET count after timeout recovery = %d, want 3", got)
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionRetriesReadinessRequestTimeoutsUntilConfiguredDeadline(t *testing.T) {
	clock := newManualClock(time.Unix(625, 0))
	manual := newControlledTimer()
	var calls atomic.Int32
	observed := make(chan int, 8)
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		call := int(calls.Add(1))
		observed <- call
		return nil, context.DeadlineExceeded
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now),
		WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second),
		WithReadinessTimeout(3*time.Second),
		WithTransport(transport),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, observed, "initial timeout"); got != 1 {
		t.Fatalf("initial timeout request=%d, want 1", got)
	}
	waitForReadinessReset(t, manual, session.done, "initial timeout")
	clock.Advance(time.Second)
	manual.Fire()
	if got := receiveTestValue(t, observed, "second timeout"); got != 2 {
		t.Fatalf("second timeout request=%d, want 2", got)
	}
	waitForReadinessReset(t, manual, session.done, "second timeout")
	clock.Advance(2 * time.Second)
	manual.Fire()
	waitForSessionDone(t, session.done, "readiness timeout deadline")
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count after timeout deadline=%d, want 2", got)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionRetriesRepeatedReadinessRefusalsUntilConfiguredDeadline(t *testing.T) {
	clock := newManualClock(time.Unix(625, 0))
	manual := newControlledTimer()
	var calls atomic.Int32
	observed := make(chan int, 4)
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		call := int(calls.Add(1))
		observed <- call
		return nil, syscall.ECONNREFUSED
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now),
		WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second),
		WithReadinessTimeout(3*time.Second),
		WithTransport(transport),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, observed, "initial refusal"); got != 1 {
		t.Fatalf("initial refusal request=%d, want 1", got)
	}
	waitForReadinessReset(t, manual, session.done, "initial refusal")
	for want := 2; want <= 3; want++ {
		clock.Advance(time.Second)
		manual.Fire()
		if got := receiveTestValue(t, observed, fmt.Sprintf("readiness refusal %d", want)); got != want {
			t.Fatalf("readiness refusal request=%d, want %d", got, want)
		}
		if want < 3 {
			waitForReadinessReset(t, manual, session.done, fmt.Sprintf("readiness refusal %d", want))
		}
	}
	waitForReadinessReset(t, manual, session.done, "readiness refusal 3")
	clock.Advance(time.Second)
	manual.Fire()
	waitForSessionDone(t, session.done, "readiness deadline")
	if got := calls.Load(); got != 3 {
		t.Fatalf("refusal request count=%d, want exact readiness count 3", got)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}
