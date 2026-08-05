package fzfsidecar

import (
	"context"
	"errors"
	"time"
)

func (session *Session) finishRun(ctx context.Context, stopReason *ObserverStopReason, fallback ObserverStopReason) error {
	if ctx.Err() != nil {
		cause := ObserverStopReason(session.stopCause.Load())
		if cause == 0 {
			cause = ObserverStopContextCanceled
			session.recordStopCause(cause)
		}
		*stopReason = cause
	} else {
		*stopReason = fallback
	}
	return nil
}

func (session *Session) recordStopCause(reason ObserverStopReason) {
	session.stopCause.CompareAndSwap(0, uint32(reason))
}

func (session *Session) getState(ctx context.Context, phase ObserverPhase) (fzfState, error) {
	session.getAttempts++
	attempt := session.getAttempts
	started := session.now()
	state, diagnostic, err := session.client.getStateResult(ctx)
	if err == nil || ctx == nil || ctx.Err() == nil {
		kind := classifyObserverOperation(ObserverGetSuccess, ObserverGetTransient, ObserverGetTerminal, err)
		if diagnostic.reason == 0 {
			diagnostic = diagnosticForError(err)
		}
		session.observe(ObserverEvent{Kind: kind, Method: ObserverMethodGet, Phase: phase,
			Status: observerStatus(kind), Reason: diagnostic.reason, HTTPStatus: diagnostic.status,
			Attempt: attempt, Duration: observerDuration(started, session.now())})
	}
	return state, err
}

func (session *Session) postState(ctx context.Context, phase ObserverPhase, state fzfState) error {
	session.postAttempts++
	attempt := session.postAttempts
	started := session.now()
	diagnostic, err := session.client.postStateResult(ctx, state)
	if err == nil || ctx == nil || ctx.Err() == nil {
		kind := classifyObserverOperation(ObserverPostSuccess, ObserverPostTransient, ObserverPostTerminal, err)
		if diagnostic.reason == 0 {
			diagnostic = diagnosticForError(err)
		}
		session.observe(ObserverEvent{Kind: kind, Method: ObserverMethodPost, Phase: phase,
			Status: observerStatus(kind), Reason: diagnostic.reason, HTTPStatus: diagnostic.status,
			Attempt: attempt, Duration: observerDuration(started, session.now())})
	}
	return err
}

func (session *Session) observe(event ObserverEvent) {
	if session.observer != nil {
		session.observer.Observe(event)
	}
}

func classifyObserverOperation(success, transient, terminal ObserverEventKind, err error) ObserverEventKind {
	if err == nil {
		return success
	}
	if errors.Is(err, errTransientCycle) || isBoundedTransportError(err) {
		return transient
	}
	return terminal
}

func observerStatus(kind ObserverEventKind) ObserverStatus {
	switch kind {
	case ObserverGetSuccess, ObserverPostSuccess:
		return ObserverStatusSuccess
	case ObserverGetTransient, ObserverPostTransient:
		return ObserverStatusTransient
	case ObserverGetTerminal, ObserverPostTerminal:
		return ObserverStatusTerminal
	default:
		return 0
	}
}

func observerDuration(started, finished time.Time) time.Duration {
	duration := finished.Sub(started)
	if duration < 0 {
		return 0
	}
	if duration > selectedPaginationTimeout {
		return selectedPaginationTimeout
	}
	return duration
}
