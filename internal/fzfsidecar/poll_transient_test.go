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

func TestSessionRetriesStartupBusy503UntilStateIsStable(t *testing.T) {
	clock := newManualClock(time.Unix(300, 0))
	manual := newControlledTimer()
	var gets atomic.Int32
	var posts atomic.Int32
	getObserved := make(chan int32, 96)
	postObserved := make(chan struct{}, 1)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			posts.Add(1)
			postObserved <- struct{}{}
			return response(http.StatusOK, "text/plain", "ok"), nil
		}
		call := gets.Add(1)
		getObserved <- call
		if call <= 2 {
			return response(http.StatusServiceUnavailable, "text/plain", "fzf busy"), nil
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

	if got := receiveTestValue(t, getObserved, "busy GET 1"); got != 1 {
		t.Fatalf("initial GET number = %d, want 1", got)
	}
	waitForBusyRetryReset(t, manual, session.done, "first")
	clock.Advance(transientRetryInterval)
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "busy GET 2"); got != 2 {
		t.Fatalf("busy GET number after first retry = %d, want 2", got)
	}
	waitForBusyRetryReset(t, manual, session.done, "second")
	clock.Advance(transientRetryInterval)
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "stable GET"); got != 3 {
		t.Fatalf("stable GET number = %d, want 3", got)
	}
	select {
	case <-postObserved:
	case <-time.After(time.Second):
		t.Fatalf("stable POSTs = %d, want 1", posts.Load())
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionRetries79TransientBusyCyclesBeforeValidState(t *testing.T) {
	clock := newManualClock(time.Unix(400, 0))
	manual := newControlledTimer()
	var gets atomic.Int32
	var posts atomic.Int32
	getObserved := make(chan int32, 96)
	postObserved := make(chan struct{}, 1)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			posts.Add(1)
			postObserved <- struct{}{}
			return response(http.StatusOK, "text/plain", "ok"), nil
		}
		call := gets.Add(1)
		getObserved <- call
		if call <= 79 {
			return response(http.StatusServiceUnavailable, "text/plain", "fzf busy"), nil
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

	for attempt := 1; attempt <= 79; attempt++ {
		if got := receiveTestValue(t, getObserved, fmt.Sprintf("busy GET %d", attempt)); got != int32(attempt) {
			t.Fatalf("GET number for attempt %d = %d, want %d", attempt, got, attempt)
		}
		waitForBusyRetryReset(t, manual, session.done, fmt.Sprintf("retry %d", attempt))
		clock.Advance(transientRetryInterval)
		manual.Fire()
	}
	if got := receiveTestValue(t, getObserved, "stable GET after busy window"); got != 80 {
		t.Fatalf("GET number after 79 transient cycles = %d, want 80", got)
	}
	select {
	case <-postObserved:
	case <-time.After(time.Second):
		t.Fatalf("valid state was not posted; posts=%d", posts.Load())
	}
	if got := gets.Load(); got != 80 {
		t.Fatalf("GETs after valid state = %d, want 80", got)
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionSoftStopsWhenTransientBusyWindowExpires(t *testing.T) {
	clock := newManualClock(time.Unix(500, 0))
	manual := newControlledTimer()
	var gets atomic.Int32
	getObserved := make(chan int32, 96)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			return response(http.StatusOK, "text/plain", "unexpected post"), nil
		}
		call := gets.Add(1)
		getObserved <- call
		return response(http.StatusServiceUnavailable, "text/plain", "fzf busy"), nil
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now),
		WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second), WithReadinessTimeout(time.Minute), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())

	for attempt := 1; attempt <= 80; attempt++ {
		if got := receiveTestValue(t, getObserved, fmt.Sprintf("expired-window GET %d", attempt)); got != int32(attempt) {
			t.Fatalf("GET number for attempt %d = %d, want %d", attempt, got, attempt)
		}
		waitForBusyRetryReset(t, manual, session.done, fmt.Sprintf("retry %d", attempt))
		clock.Advance(transientRetryInterval)
		manual.Fire()
	}
	if got := receiveTestValue(t, getObserved, "expired-window GET final"); got != 81 {
		t.Fatalf("GET number at expired window = %d, want 81", got)
	}
	select {
	case <-session.done:
	case <-time.After(time.Second):
		t.Fatal("session did not soft-stop at the transient window deadline")
	}
	if got := gets.Load(); got != 81 {
		t.Fatalf("GETs after transient window = %d", got)
	}
	manual.Fire()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionResetsTransientWindowAfterValidState(t *testing.T) {
	clock := newManualClock(time.Unix(550, 0))
	manual := newControlledTimer()
	var gets atomic.Int32
	var posts atomic.Int32
	getObserved := make(chan int32, 4)
	postObserved := make(chan struct{}, 2)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			posts.Add(1)
			postObserved <- struct{}{}
			return response(http.StatusOK, "text/plain", "ok"), nil
		}
		call := gets.Add(1)
		getObserved <- call
		switch call {
		case 1:
			return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":1,"selected":[]}`), nil
		case 2:
			return response(http.StatusServiceUnavailable, "text/plain", "fzf busy"), nil
		case 3:
			return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":2,"selected":[]}`), nil
		case 4:
			return response(http.StatusServiceUnavailable, "text/plain", "fzf busy"), nil
		default:
			return nil, errors.New("unexpected GET after reset check")
		}
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now),
		WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second), WithReadinessTimeout(10*time.Second), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, getObserved, "initial valid GET"); got != 1 {
		t.Fatalf("initial GET=%d, want 1", got)
	}
	waitForTestSignal(t, postObserved, "initial POST")
	if got := receiveTestValue(t, manual.resetEvents, "initial valid timer reset"); got != time.Second {
		t.Fatalf("initial cycle reset=%v, want 1s", got)
	}
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "transient GET"); got != 2 {
		t.Fatalf("transient GET=%d, want 2", got)
	}
	waitForBusyRetryReset(t, manual, session.done, "reset retry")
	clock.Advance(transientRetryInterval)
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "valid GET after transient"); got != 3 {
		t.Fatalf("valid GET after transient=%d, want 3", got)
	}
	waitForTestSignal(t, postObserved, "POST after transient")
	if got := receiveTestValue(t, manual.resetEvents, "post-valid timer reset"); got != time.Second {
		t.Fatalf("post-valid reset=%v, want 1s", got)
	}
	clock.Advance(transientRetryWindow)
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "post-reset transient GET"); got != 4 {
		t.Fatalf("post-reset transient GET=%d, want 4", got)
	}
	waitForBusyRetryReset(t, manual, session.done, "post-reset retry")
	select {
	case <-session.done:
		t.Fatal("session stopped despite valid-state transient window reset")
	default:
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := posts.Load(); got != 2 {
		t.Fatalf("POST count=%d, want 2", got)
	}
}

func TestSessionRetriesChangedStateAfterTransientPostUntilSuccess(t *testing.T) {
	clock := newManualClock(time.Unix(800, 0))
	manual := newControlledTimer()
	observer := &recordingObserver{}
	var gets, posts, successfulPosts atomic.Int32
	getObserved := make(chan int32, 128)
	postObserved := make(chan int32, 128)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			call := gets.Add(1)
			getObserved <- call
			if call == 1 {
				return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":1,"selected":[]}`), nil
			}
			return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":2,"selected":[]}`), nil
		}
		call := posts.Add(1)
		postObserved <- call
		if call == 1 || call == 81 {
			successfulPosts.Add(1)
			return response(http.StatusOK, "text/plain", "ok"), nil
		}
		if call%2 == 0 {
			return nil, syscall.ECONNRESET
		}
		return response(http.StatusServiceUnavailable, "text/plain", "busy"), nil
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now),
		WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second), WithReadinessTimeout(10*time.Second),
		WithTransport(transport), WithObserver(observer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, getObserved, "initial POST retry GET"); got != 1 {
		t.Fatalf("initial GET=%d, want 1", got)
	}
	if got := receiveTestValue(t, postObserved, "initial POST retry POST"); got != 1 {
		t.Fatalf("initial POST=%d, want 1", got)
	}
	if got := receiveTestValue(t, manual.resetEvents, "initial POST retry timer reset"); got != time.Second {
		t.Fatalf("initial timer=%v, want 1s", got)
	}
	manual.Fire()

	for wantPost := int32(2); wantPost <= 80; wantPost++ {
		if got := receiveTestValue(t, getObserved, fmt.Sprintf("transient POST GET %d", wantPost)); got != wantPost {
			t.Fatalf("GET before transient POST %d=%d, want %d", wantPost, got, wantPost)
		}
		if got := receiveTestValue(t, postObserved, fmt.Sprintf("transient POST %d", wantPost)); got != wantPost {
			t.Fatalf("transient POST=%d, want %d", got, wantPost)
		}
		waitForBusyRetryReset(t, manual, session.done, fmt.Sprintf("POST retry %d", wantPost))
		clock.Advance(transientRetryInterval)
		manual.Fire()
	}
	if got := receiveTestValue(t, getObserved, "successful retry GET"); got != 81 {
		t.Fatalf("GET before successful POST=%d, want 81", got)
	}
	if got := receiveTestValue(t, postObserved, "successful retry POST"); got != 81 {
		t.Fatalf("successful retry POST=%d, want 81", got)
	}
	if got := receiveTestValue(t, manual.resetEvents, "successful POST timer reset"); got != time.Second {
		t.Fatalf("successful POST timer=%v, want 1s", got)
	}
	if got := successfulPosts.Load(); got != 2 {
		t.Fatalf("successful POST count=%d, want exactly 2", got)
	}
	if got := posts.Load(); got != 81 {
		t.Fatalf("total POST count=%d, want 81", got)
	}
	if got := countObserverEvents(observer.Events(), ObserverPostTransient); got != 79 {
		t.Fatalf("observer transient POST count=%d, want 79", got)
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionRetriesReadyConnectionRefusalAsBoundedTransient(t *testing.T) {
	clock := newManualClock(time.Unix(850, 0))
	manual := newControlledTimer()
	var gets, posts atomic.Int32
	getObserved := make(chan int32, 4)
	postObserved := make(chan int32, 2)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			call := gets.Add(1)
			getObserved <- call
			switch call {
			case 1:
				return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":1,"selected":[]}`), nil
			case 2:
				return nil, syscall.ECONNREFUSED
			default:
				return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":2,"selected":[]}`), nil
			}
		}
		call := posts.Add(1)
		postObserved <- call
		return response(http.StatusOK, "text/plain", "ok"), nil
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now), WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second), WithReadinessTimeout(10*time.Second), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, getObserved, "ready refusal initial GET"); got != 1 {
		t.Fatalf("initial GET=%d, want 1", got)
	}
	if got := receiveTestValue(t, postObserved, "ready refusal initial POST"); got != 1 {
		t.Fatalf("initial POST=%d, want 1", got)
	}
	receiveTestValue(t, manual.resetEvents, "ready refusal timer reset")
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "ready refusal GET"); got != 2 {
		t.Fatalf("bounded transport GET=%d, want 2", got)
	}
	waitForBusyRetryReset(t, manual, session.done, "bounded transport retry")
	clock.Advance(transientRetryInterval)
	manual.Fire()
	if got := receiveTestValue(t, getObserved, "ready refusal recovered GET"); got != 3 {
		t.Fatalf("recovered GET=%d, want 3", got)
	}
	if got := receiveTestValue(t, postObserved, "ready refusal recovered POST"); got != 2 {
		t.Fatalf("recovered POST=%d, want 2", got)
	}
	select {
	case <-session.done:
		t.Fatal("session stopped after a recoverable bounded transport error")
	default:
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSessionStopsWhenTransientPostWindowExpires(t *testing.T) {
	clock := newManualClock(time.Unix(900, 0))
	manual := newControlledTimer()
	observer := &recordingObserver{}
	var gets, posts atomic.Int32
	getObserved := make(chan int32, 128)
	postObserved := make(chan int32, 128)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodGet {
			call := gets.Add(1)
			getObserved <- call
			if call == 1 {
				return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":1,"selected":[]}`), nil
			}
			return response(http.StatusOK, "application/json", `{"totalCount":2,"matchCount":2,"selected":[]}`), nil
		}
		call := posts.Add(1)
		postObserved <- call
		if call == 1 {
			return response(http.StatusOK, "text/plain", "ok"), nil
		}
		return response(http.StatusServiceUnavailable, "text/plain", "busy"), nil
	})
	session, err := New(protocol.PickerCD,
		WithClock(clock.Now),
		WithTimer(func(time.Duration) timer { return manual }),
		WithInterval(time.Second), WithReadinessTimeout(time.Minute),
		WithTransport(transport), WithObserver(observer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	receiveTestValue(t, getObserved, "expired POST initial GET")
	receiveTestValue(t, postObserved, "expired POST initial POST")
	receiveTestValue(t, manual.resetEvents, "expired POST initial timer reset")
	manual.Fire()
	for wantPost := int32(2); wantPost <= 82; wantPost++ {
		if got := receiveTestValue(t, getObserved, fmt.Sprintf("expired POST GET %d", wantPost)); got != wantPost {
			t.Fatalf("GET before expired POST %d=%d, want %d", wantPost, got, wantPost)
		}
		if got := receiveTestValue(t, postObserved, fmt.Sprintf("expired POST %d", wantPost)); got != wantPost {
			t.Fatalf("expired-window POST=%d, want %d", got, wantPost)
		}
		if wantPost < 82 {
			waitForBusyRetryReset(t, manual, session.done, fmt.Sprintf("expired POST retry %d", wantPost))
			clock.Advance(transientRetryInterval)
			manual.Fire()
		}
	}
	select {
	case <-session.done:
	case <-time.After(time.Second):
		t.Fatal("session did not stop after transient POST window expiry")
	}
	events := observer.Events()
	if got := countObserverEvents(events, ObserverPostTransient); got != 81 {
		t.Fatalf("observer transient POST count=%d, want 81", got)
	}
	if got := events[len(events)-1]; got.Kind != ObserverStop || got.StopReason != ObserverStopTransientWindow {
		t.Fatalf("final observer event=%+v, want transient-window stop", got)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func countObserverEvents(events []ObserverEvent, kind ObserverEventKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func TestSessionTransientWindowResetsAfterValidStateCycle(t *testing.T) {
	start := time.Unix(700, 0)
	window := transientWindow{}
	if !window.allow(start) {
		t.Fatal("first transient was rejected")
	}
	if window.allow(start.Add(transientRetryWindow)) {
		t.Fatal("transient at the window deadline was accepted")
	}
	window.reset()
	if !window.allow(start.Add(transientRetryWindow)) {
		t.Fatal("transient window did not reset after a valid state")
	}
}
