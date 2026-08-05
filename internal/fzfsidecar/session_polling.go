package fzfsidecar

import (
	"context"
	"errors"
	"syscall"
	"time"
)

type transientWindow struct {
	started time.Time
	active  bool
}

func (window *transientWindow) allow(now time.Time) bool {
	if !window.active {
		window.started = now
		window.active = true
		return true
	}
	return now.Sub(window.started) < transientRetryWindow
}

func (window *transientWindow) reset() {
	window.started = time.Time{}
	window.active = false
}

func waitForReadiness(ctx context.Context, intervalTimer timer, interval time.Duration, deadline time.Time, now func() time.Time) bool {
	if now().After(deadline) {
		return false
	}
	return waitForNextCycle(ctx, intervalTimer, interval) && now().Before(deadline)
}

func waitForNextCycle(ctx context.Context, intervalTimer timer, interval time.Duration) bool {
	if !resetSessionTimer(intervalTimer, interval) {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case _, ok := <-intervalTimer.C():
		if !ok {
			return false
		}
		return true
	}
}

func resetSessionTimer(intervalTimer timer, interval time.Duration) bool {
	intervalTimer.Reset(interval)
	return true
}

func isReadinessTransportError(err error) bool {
	return isConnectionRefused(err) || errors.Is(err, context.DeadlineExceeded)
}

func isBoundedTransportError(err error) bool {
	return isTransientTransportCause(err) || isConnectionRefused(err) || isBoundedHTTPTransportError(err)
}

func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.Errno(10061)
}
