package fzfsidecar

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

const (
	defaultInterval         = 25 * time.Millisecond
	defaultReadinessTimeout = 2 * time.Second
	requestTimeout          = 500 * time.Millisecond
	dialTimeout             = 250 * time.Millisecond
	maxResponseBytes        = 1 << 20
	// Busy fzf cycles retry at a fixed cadence for one readiness-scale window.
	transientRetryInterval = 25 * time.Millisecond
	transientRetryWindow   = defaultReadinessTimeout
)

// Option changes a Session seam. Production behavior is fixed by the defaults;
// options make lifecycle, transport, and secret-free diagnostics injectable.
type Option func(*sessionOptions) error

// ObserverEventKind is the closed set of sidecar diagnostic event categories.
// Events contain no URLs, credentials, request bodies, selected state, or raw
// error text; safe response status metadata is carried separately.
type ObserverEventKind uint8

const (
	ObserverGetSuccess ObserverEventKind = iota + 1
	ObserverGetTransient
	ObserverGetTerminal
	ObserverPostSuccess
	ObserverPostTransient
	ObserverPostTerminal
	ObserverStop
)

// ObserverStopReason is the closed set of reasons a sidecar polling loop ends.
type ObserverStopReason uint8

const (
	ObserverStopContextCanceled ObserverStopReason = iota + 1
	ObserverStopReadinessTimeout
	ObserverStopTransientWindow
	ObserverStopTerminal
	ObserverStopRequested
)

// ObserverMethod identifies the sidecar HTTP operation represented by an event.
type ObserverMethod uint8

const (
	ObserverMethodGet ObserverMethod = iota + 1
	ObserverMethodPost
)

// ObserverPhase identifies whether an operation happened while the session was
// waiting for fzf to become ready or polling an already-ready listener.
type ObserverPhase uint8

const (
	ObserverPhaseReadiness ObserverPhase = iota + 1
	ObserverPhaseReady
)

// ObserverStatus is the safe retry classification for an operation result.
type ObserverStatus uint8

const (
	ObserverStatusSuccess ObserverStatus = iota + 1
	ObserverStatusTransient
	ObserverStatusTerminal
)

// ObserverReason is a closed, secret-free description of an operation result.
// HTTPStatus carries the numeric response code when a response was received.
type ObserverReason uint8

const (
	ObserverReasonHTTPStatus ObserverReason = iota + 1
	ObserverReasonTransport
	ObserverReasonResponse
	ObserverReasonResponseTooLarge
	ObserverReasonInvalidMIME
	ObserverReasonInvalidJSON
	ObserverReasonInvalidState
	ObserverReasonInvalidAction
	ObserverReasonInconsistentSnapshot
	ObserverReasonContextCanceled
)

// ObserverEvent contains only closed-category, timing, and attempt-count
// diagnostics. It deliberately excludes URLs, API keys, request bodies,
// selected state, and raw error text.
type ObserverEvent struct {
	Kind       ObserverEventKind
	Method     ObserverMethod
	Phase      ObserverPhase
	Status     ObserverStatus
	Reason     ObserverReason
	HTTPStatus int
	Attempt    uint64
	Duration   time.Duration
	StopReason ObserverStopReason
}

// Observer receives secret-free sidecar diagnostics.
type Observer interface {
	Observe(ObserverEvent)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(ObserverEvent)

func (observer ObserverFunc) Observe(event ObserverEvent) { observer(event) }

type sessionOptions struct {
	now              func() time.Time
	timer            func(time.Duration) Timer
	readinessTimeout time.Duration
	interval         time.Duration
	transport        http.RoundTripper
	dialContext      func(context.Context, string, string) (net.Conn, error)
	random           io.Reader
	reserve          func(string, string) (net.Listener, error)
	observer         Observer
}

// Timer is the clock interface used to schedule one complete polling cycle.
// Reset starts a fresh interval and discards any prior expiration; Stop is
// called once when the session ends.
type Timer interface {
	C() <-chan time.Time
	Reset(time.Duration) bool
	Stop() bool
}

type timer = Timer

type standardTimer struct {
	timer *time.Timer
}

func (timer *standardTimer) C() <-chan time.Time {
	return timer.timer.C
}

func (timer *standardTimer) Reset(interval time.Duration) bool {
	return timer.timer.Reset(interval)
}

func (timer *standardTimer) Stop() bool {
	return timer.timer.Stop()
}

func newStandardTimer(time.Duration) Timer {
	timer := time.NewTimer(0)
	<-timer.C
	return &standardTimer{timer: timer}
}

func defaultSessionOptions() sessionOptions {
	return sessionOptions{
		now:              time.Now,
		timer:            newStandardTimer,
		readinessTimeout: defaultReadinessTimeout,
		interval:         defaultInterval,
		random:           rand.Reader,
		reserve:          net.Listen,
	}
}

// WithClock injects the clock used for readiness deadlines.
func WithClock(now func() time.Time) Option {
	return func(options *sessionOptions) error {
		if now == nil {
			return errors.New("fzf sidecar: nil clock")
		}
		options.now = now
		return nil
	}
}

// WithTimer injects the resettable timer used for readiness and polling.
func WithTimer(factory func(time.Duration) Timer) Option {
	return func(options *sessionOptions) error {
		if factory == nil {
			return errors.New("fzf sidecar: nil timer factory")
		}
		options.timer = factory
		return nil
	}
}

// WithReadinessTimeout injects the deadline used while waiting for fzf.
func WithReadinessTimeout(timeout time.Duration) Option {
	return func(options *sessionOptions) error {
		if timeout < 0 {
			return errors.New("fzf sidecar: negative readiness timeout")
		}
		options.readinessTimeout = timeout
		return nil
	}
}

// WithInterval injects the readiness and post-readiness poll interval.
func WithInterval(interval time.Duration) Option {
	return func(options *sessionOptions) error {
		if interval <= 0 {
			return errors.New("fzf sidecar: non-positive interval")
		}
		options.interval = interval
		return nil
	}
}

// WithTransport injects an HTTP round tripper for tests.
func WithTransport(transport http.RoundTripper) Option {
	return func(options *sessionOptions) error {
		if transport == nil {
			return errors.New("fzf sidecar: nil HTTP transport")
		}
		if typed, ok := transport.(*http.Transport); ok && typed == nil {
			return errors.New("fzf sidecar: nil HTTP transport")
		}
		options.transport = transport
		return nil
	}
}

// WithDialer injects the context-aware dialer used by the production
// transport.
func WithDialer(dialContext func(context.Context, string, string) (net.Conn, error)) Option {
	return func(options *sessionOptions) error {
		if dialContext == nil {
			return errors.New("fzf sidecar: nil dialer")
		}
		options.dialContext = dialContext
		return nil
	}
}

// WithRandomSource injects the source used to generate the API key.
func WithRandomSource(random io.Reader) Option {
	return func(options *sessionOptions) error {
		if random == nil {
			return errors.New("fzf sidecar: nil random source")
		}
		options.random = random
		return nil
	}
}

// WithPortReservation injects the ephemeral-port reservation function.
func WithPortReservation(reserve func(string, string) (net.Listener, error)) Option {
	return func(options *sessionOptions) error {
		if reserve == nil {
			return errors.New("fzf sidecar: nil port reservation")
		}
		options.reserve = reserve
		return nil
	}
}

// WithObserver injects the optional secret-free diagnostic observer.
func WithObserver(observer Observer) Option {
	return func(options *sessionOptions) error {
		if observer == nil {
			return errors.New("fzf sidecar: nil observer")
		}
		options.observer = observer
		return nil
	}
}
