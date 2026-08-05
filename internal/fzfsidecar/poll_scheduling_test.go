package fzfsidecar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

func TestSessionWaitsForFreshTimerAfterSlowPollingCycle(t *testing.T) {
	manual := newControlledTimer()
	transport := newSlowCycleTransport()
	session, err := New(protocol.PickerCD, WithTimer(func(time.Duration) Timer { return manual }), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	waitForTestSignal(t, transport.postStarted, "slow POST start")
	for range 8 {
		manual.Fire()
	}
	close(transport.releasePost)
	if got := receiveTestValue(t, manual.resetEvents, "post completion timer reset"); got != defaultInterval {
		t.Fatalf("post-completion timer interval = %v, want %v", got, defaultInterval)
	}
	select {
	case <-transport.secondGET:
		t.Fatal("stale timer signals started a second GET")
	default:
	}
	manual.Fire()
	waitForTestSignal(t, transport.secondGET, "second GET")
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := manual.stopCalls.Load(); got != 1 {
		t.Fatalf("timer Stop calls = %d, want 1", got)
	}
	if got := transport.getCalls.Load(); got != 2 {
		t.Fatalf("GET calls = %d, want 2", got)
	}
}

func TestSessionRepeatedStartOwnsOnePollingGoroutine(t *testing.T) {
	manual := newManualTicker()
	server := newFakeFZFServer(t, func(int) string { return `{"totalCount":1,"matchCount":1,"selected":[]}` })
	session := newServerSession(t, protocol.PickerCD, server, manual)
	for range 20 {
		session.Start(context.Background())
	}
	if got := receiveTestValue(t, server.posted, "initial POST"); got != "change-list-label:1/1" {
		t.Fatalf("initial action = %q, want %q", got, "change-list-label:1/1")
	}
	if got := receiveTestValue(t, server.gets, "initial GET"); got != 1 {
		t.Fatalf("initial GET number = %d, want 1", got)
	}
	manual.Tick(time.Now())
	if got := receiveTestValue(t, server.gets, "second GET"); got != 2 {
		t.Fatalf("second GET number = %d, want 2", got)
	}
	session.Stop()
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if _, got := server.counts(); got != 1 {
		t.Fatalf("actions for repeated Start = %d, want 1", got)
	}
}

func waitForBusyRetryReset(t *testing.T, manual *controlledTimer, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case got := <-manual.resetEvents:
		if got != transientRetryInterval {
			t.Fatalf("%s delay = %v, want %v", name, got, transientRetryInterval)
		}
	case <-done:
		t.Fatalf("session stopped before %s timer reset", name)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s timer reset", name)
	}
}

func waitForReadinessReset(t *testing.T, manual *controlledTimer, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case got := <-manual.resetEvents:
		if got != time.Second {
			t.Fatalf("%s delay = %v, want configured readiness interval %v", name, got, time.Second)
		}
	case <-done:
		t.Fatalf("session stopped before %s readiness timer reset", name)
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s readiness timer reset", name)
	}
}

func waitForSessionDone(t *testing.T, done <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func TestReadinessRetryClassifierRecognizesWrappedRefusalWithoutTransportStringMatching(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "wrapped refusal", err: fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED), want: true},
		{name: "wrapped reset", err: fmt.Errorf("dial failed: %w", syscall.ECONNRESET), want: false},
		{name: "refusal text", err: errors.New("connection refused"), want: false},
		{name: "HTTP status", err: errInvalidStatus, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isReadinessTransportError(test.err); got != test.want {
				t.Fatalf("isReadinessTransportError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestReadinessStopsImmediatelyForNonRefusedFailures(t *testing.T) {
	tests := []struct {
		name     string
		response *http.Response
		err      error
	}{
		{name: "dns", err: &net.DNSError{Err: "no such host", Name: "127.0.0.1"}},
		{name: "reset", err: syscall.ECONNRESET},
		{name: "timeout", err: syscall.ETIMEDOUT},
		{name: "auth", response: response(http.StatusUnauthorized, "text/plain", "no")},
		{name: "status", response: response(http.StatusServiceUnavailable, "text/plain", "no")},
		{name: "schema", response: response(http.StatusOK, "application/json", `{"totalCount":"bad"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				if calls.Add(1) > 1 {
					cancel()
					return nil, context.Canceled
				}
				if test.err != nil {
					return nil, test.err
				}
				return test.response, nil
			})
			clock := newManualClock(time.Unix(100, 0))
			closed := newClosedTicker()
			session, err := New(protocol.PickerCD, WithClock(clock.Now), WithTimer(func(time.Duration) timer { return closed }), WithReadinessTimeout(time.Hour), WithTransport(transport))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			session.Start(parent)
			if err := session.Wait(); err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("requests before soft stop = %d, want 1", got)
			}
		})
	}
}

func TestSessionReadinessExpiryIsSoft(t *testing.T) {
	clock := newManualClock(time.Unix(200, 0))
	manual := newManualTicker()
	var calls atomic.Int32
	observed := make(chan struct{}, 1)
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		observed <- struct{}{}
		return nil, fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED)
	})
	session, err := New(protocol.PickerCD, WithClock(clock.Now), WithTimer(func(time.Duration) timer { return manual }), WithInterval(time.Second), WithReadinessTimeout(time.Second), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	receiveTestValue(t, observed, "readiness expiry request")
	select {
	case <-session.done:
		t.Fatal("session expired before the fake readiness deadline")
	default:
	}
	clock.Advance(time.Second)
	manual.Tick(time.Time{})
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestSessionStopsSoftlyAfterAReadyStateFails(t *testing.T) {
	manual := newManualTicker()
	var calls atomic.Int32
	observed := make(chan int, 4)
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		call := calls.Add(1)
		observed <- int(call)
		switch call {
		case 1:
			return response(http.StatusOK, "application/json", `{"totalCount":10,"matchCount":3,"selected":[]}`), nil
		case 2:
			return response(http.StatusOK, "text/plain", "ok"), nil
		case 3:
			return response(http.StatusUnauthorized, "text/plain", "unauthorized"), nil
		default:
			return nil, errors.New("request after soft stop")
		}
	})
	session, err := New(protocol.PickerCD, WithTimer(func(time.Duration) timer { return manual }), WithInterval(time.Millisecond), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if got := receiveTestValue(t, observed, "soft-stop GET 1"); got != 1 {
		t.Fatalf("first request number = %d, want 1", got)
	}
	if got := receiveTestValue(t, observed, "soft-stop POST"); got != 2 {
		t.Fatalf("second request number = %d, want 2", got)
	}
	manual.Tick(time.Now())
	if got := receiveTestValue(t, observed, "soft-stop GET 2"); got != 3 {
		t.Fatalf("third request number = %d, want 3", got)
	}
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	manual.Tick(time.Now())
	if got := calls.Load(); got != 3 {
		t.Fatalf("requests after soft stop = %d, want 3", got)
	}
}

func TestSessionUsesTheDefaultIntervalAndStopsTickerOnce(t *testing.T) {
	observedTicker := newObservingTicker()
	intervals := make(chan time.Duration, 1)
	var factoryCalls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, "text/plain", "no"), nil
	})
	session, err := New(protocol.PickerCD, WithTimer(func(interval time.Duration) timer {
		factoryCalls.Add(1)
		intervals <- interval
		return observedTicker
	}), WithTransport(transport))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	session.Start(context.Background())
	if err := session.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got := receiveTestValue(t, intervals, "default timer interval"); got != defaultInterval {
		t.Fatalf("ticker interval = %v, want %v", got, defaultInterval)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("ticker factory calls = %d, want 1", got)
	}
	if got := observedTicker.stopCalls.Load(); got != 1 {
		t.Fatalf("ticker Stop calls = %d, want 1", got)
	}
}
