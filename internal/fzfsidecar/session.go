package fzfsidecar

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var errInvalidPicker = errors.New("fzf sidecar: invalid picker")

// Session owns the short-lived fzf HTTP polling sidecar.
type Session struct {
	picker  protocol.Picker
	address string
	apiKey  string
	client  *client

	now              func() time.Time
	timer            func(time.Duration) timer
	readinessTimeout time.Duration
	interval         time.Duration
	observer         Observer
	getAttempts      uint64
	postAttempts     uint64

	mu        sync.Mutex
	started   bool
	stopped   bool
	waited    bool
	stopCause atomic.Uint32
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	runErr    error
}

// New creates a session and reserves one numeric IPv4 loopback port.
func New(picker protocol.Picker, optionList ...Option) (session *Session, err error) {
	if picker != protocol.PickerCD && picker != protocol.PickerCP {
		return nil, errInvalidPicker
	}

	options := defaultSessionOptions()
	for _, option := range optionList {
		if option == nil {
			return nil, errors.New("fzf sidecar: nil option")
		}
		if err := option(&options); err != nil {
			return nil, err
		}
	}
	if options.now == nil || options.timer == nil || options.random == nil || options.reserve == nil || options.interval <= 0 || options.readinessTimeout < 0 {
		return nil, errors.New("fzf sidecar: incomplete options")
	}

	reservation, err := options.reserve("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fzf sidecar: reserve port: %w", err)
	}
	if reservation == nil {
		return nil, errors.New("fzf sidecar: port reservation returned nil listener")
	}
	defer func() {
		if closeErr := reservation.Close(); closeErr != nil {
			session = nil
			closeErr = fmt.Errorf("fzf sidecar: close port reservation: %w", closeErr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()

	port, err := loopbackPort(reservation)
	if err != nil {
		return nil, err
	}
	var rawKey [32]byte
	if _, err := io.ReadFull(options.random, rawKey[:]); err != nil {
		return nil, fmt.Errorf("fzf sidecar: generate API key: %w", err)
	}

	apiKey := base64.RawURLEncoding.EncodeToString(rawKey[:])
	address := fmt.Sprintf("127.0.0.1:%d", port)
	httpClient, idleCloser := newHTTPClient(options)
	return &Session{
		picker:           picker,
		address:          address,
		apiKey:           apiKey,
		client:           newClient(address, apiKey, httpClient, idleCloser, picker),
		now:              options.now,
		timer:            options.timer,
		readinessTimeout: options.readinessTimeout,
		interval:         options.interval,
		observer:         options.observer,
		done:             make(chan struct{}),
	}, nil
}

// Address returns the numeric IPv4 loopback address and port.
func (session *Session) Address() string {
	if session == nil {
		return ""
	}
	return session.address
}

// APIKey returns the raw URL-safe API key used for fzf requests.
func (session *Session) APIKey() string {
	if session == nil {
		return ""
	}
	return session.apiKey
}

// Start starts the single polling goroutine. The first call wins; concurrent
// or repeated calls are no-ops. Stop or Wait before Start make the session
// terminal, so no later call can create work after a completed Wait.
func (session *Session) Start(parent context.Context) {
	if session == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}

	session.mu.Lock()
	if session.started || session.stopped || session.waited {
		session.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	session.started = true
	session.ctx = ctx
	session.cancel = cancel
	session.mu.Unlock()

	go func() {
		defer cancel()
		err := session.run(ctx)
		session.mu.Lock()
		session.runErr = err
		close(session.done)
		session.mu.Unlock()
	}()
}

// Stop requests cancellation and closes reusable transport connections. It
// deliberately does not wait; callers that need the join use Wait.
func (session *Session) Stop() {
	if session == nil {
		return
	}
	session.mu.Lock()
	if !session.started {
		session.stopped = true
	}
	cancel, ctx := session.cancel, session.ctx
	session.mu.Unlock()
	if cancel != nil {
		reason := ObserverStopRequested
		if ctx != nil && ctx.Err() != nil {
			reason = ObserverStopContextCanceled
		}
		session.recordStopCause(reason)
		cancel()
	}
	session.client.closeIdleConnections()
}

// Wait joins the polling goroutine. It is safe before Start and on repeated
// calls. Poll transport and schema failures are intentionally not returned.
func (session *Session) Wait() error {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	if !session.started {
		session.waited = true
		session.mu.Unlock()
		return nil
	}
	done := session.done
	session.mu.Unlock()

	<-done
	session.mu.Lock()
	err := session.runErr
	session.mu.Unlock()
	return err
}

func (session *Session) run(ctx context.Context) error {
	defer session.client.closeIdleConnections()
	stopReason := ObserverStopRequested
	defer func() {
		session.observe(ObserverEvent{Kind: ObserverStop, StopReason: stopReason})
	}()
	intervalTimer := session.timer(session.interval)
	if intervalTimer == nil {
		stopReason = ObserverStopTerminal
		return errors.New("fzf sidecar: timer factory returned nil timer")
	}
	defer intervalTimer.Stop()

	deadline := session.now().Add(session.readinessTimeout)
	var lastLabel string
	hasLabel := false
	postPending := false
	ready := false
	var state fzfState
	haveState := false
	transient := transientWindow{}
	for {
		if !haveState {
			var err error
			phase := ObserverPhaseReadiness
			if ready {
				phase = ObserverPhaseReady
			}
			state, err = session.getState(ctx, phase)
			if err != nil {
				if ctx.Err() != nil {
					return session.finishRun(ctx, &stopReason, ObserverStopContextCanceled)
				}
				if errors.Is(err, errInconsistentSnapshot) {
					if ready {
						if !waitForNextCycle(ctx, intervalTimer, session.interval) {
							return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
						}
					} else if !waitForReadiness(ctx, intervalTimer, session.interval, deadline, session.now) {
						return session.finishRun(ctx, &stopReason, ObserverStopReadinessTimeout)
					}
					continue
				}
				if errors.Is(err, errTransientCycle) {
					if !transient.allow(session.now()) {
						return session.finishRun(ctx, &stopReason, ObserverStopTransientWindow)
					}
					if ready {
						if !waitForNextCycle(ctx, intervalTimer, transientRetryInterval) {
							return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
						}
					} else if !waitForReadiness(ctx, intervalTimer, transientRetryInterval, deadline, session.now) {
						return session.finishRun(ctx, &stopReason, ObserverStopReadinessTimeout)
					}
					continue
				}
				if ready && isBoundedTransportError(err) {
					if !transient.allow(session.now()) {
						return session.finishRun(ctx, &stopReason, ObserverStopTransientWindow)
					}
					if !waitForNextCycle(ctx, intervalTimer, transientRetryInterval) {
						return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
					}
					continue
				}
				if !ready && isReadinessTransportError(err) {
					if !waitForReadiness(ctx, intervalTimer, session.interval, deadline, session.now) {
						return session.finishRun(ctx, &stopReason, ObserverStopReadinessTimeout)
					}
					continue
				}
				return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
			}
			haveState = true
			ready = true
		}

		label, err := state.renderedLabel()
		if err != nil {
			return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
		}
		// A successful GET resets a standalone GET window. While a POST is
		// outstanding, the GET is part of that same failed cycle and must not
		// extend the POST retry window.
		if !postPending {
			transient.reset()
		}
		if !postPending && hasLabel && label == lastLabel {
			haveState = false
			if !waitForNextCycle(ctx, intervalTimer, session.interval) {
				return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
			}
			continue
		}
		if err := session.postState(ctx, ObserverPhaseReady, state); err != nil {
			if ctx.Err() != nil {
				return session.finishRun(ctx, &stopReason, ObserverStopContextCanceled)
			}
			if errors.Is(err, errTransientCycle) || isBoundedTransportError(err) {
				postPending = true
				if !transient.allow(session.now()) {
					return session.finishRun(ctx, &stopReason, ObserverStopTransientWindow)
				}
				haveState = false
				if !waitForNextCycle(ctx, intervalTimer, transientRetryInterval) {
					return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
				}
				continue
			}
			return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
		}
		transient.reset()
		lastLabel = label
		hasLabel = true
		postPending = false
		haveState = false
		if !waitForNextCycle(ctx, intervalTimer, session.interval) {
			return session.finishRun(ctx, &stopReason, ObserverStopTerminal)
		}
	}
}
