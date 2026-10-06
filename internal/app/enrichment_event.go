package app

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func (enrichment *initialEnrichment) reserveEvent(ctx context.Context) (context.Context, uint64, context.CancelCauseFunc, error) {
	var callerDone <-chan struct{}
	if ctx != nil {
		callerDone = ctx.Done()
	}
	for {
		enrichment.gate.Lock()
		if enrichment.terminal {
			err := enrichment.terminalErr
			if err == nil {
				err = context.Canceled
			}
			enrichment.gate.Unlock()
			return nil, 0, func(error) {}, err
		}
		if !enrichment.committing && enrichment.inFlight == 0 {
			var eventCtx context.Context
			var cancelEvent context.CancelCauseFunc
			if ctx == nil {
				cancelEvent = func(error) {}
			} else {
				eventCtx, cancelEvent = context.WithCancelCause(ctx)
			}
			if cancelEvent == nil {
				cancelEvent = func(error) {}
			}
			if enrichment.nextEventID == math.MaxUint64 {
				enrichment.gate.Unlock()
				return nil, 0, func(error) {}, errors.New("initial enrichment: event ID limit reached")
			}
			enrichment.nextEventID++
			eventID := enrichment.nextEventID
			enrichment.eventCancels[eventID] = cancelEvent
			enrichment.inFlight++
			enrichment.gate.Unlock()
			return eventCtx, eventID, cancelEvent, nil
		}
		changed := enrichment.stateChanged
		enrichment.gate.Unlock()
		select {
		case <-changed:
		case <-callerDone:
			return nil, 0, func(error) {}, context.Cause(ctx)
		}
	}
}

func (enrichment *initialEnrichment) resolveEvent(event protocol.Event, eventID uint64, result session.TransitionResult, err error) (error, bool) {
	enrichment.gate.Lock()
	_, active := enrichment.eventCancels[eventID]
	delete(enrichment.eventCancels, eventID)
	if !active {
		enrichment.gate.Unlock()
		return nil, false
	}
	if err != nil || enrichment.terminal {
		terminalErr := enrichment.terminalErr
		if enrichment.inFlight > 0 {
			enrichment.inFlight--
		}
		enrichment.signalLocked()
		enrichment.gate.Unlock()
		if err == nil {
			return terminalErr, false
		}
		return nil, false
	}
	generation := result.Effect.ReloadGeneration
	if generation == 0 {
		generation = result.Effect.RestoreGeneration
	}
	restore := event.Opcode == protocol.OpRestoreView && result.Effect.RestoreGeneration != 0
	enrichment.pendingEvents[eventID] = &pendingEvent{generation: generation,
		closeInput: result.Effect.ReloadGeneration != 0 || restore}
	var stopCause error
	stopSource := false
	terminalEvent := err == nil && (result.Effect.Accept || result.Effect.Abort)
	navigation := err == nil && result.Effect.ReloadGeneration != 0
	restoreDiscard := err == nil && restore && enrichment.active
	var otherEvents []context.CancelCauseFunc
	if terminalEvent {
		enrichment.terminal = true
		enrichment.discardRequested = true
		stopCause, _ = enrichment.deactivateLocked(nil)
		stopSource = true
		otherEvents = make([]context.CancelCauseFunc, 0, len(enrichment.eventCancels))
		for _, cancelEvent := range enrichment.eventCancels {
			otherEvents = append(otherEvents, cancelEvent)
		}
	} else if restoreDiscard {
		enrichment.discardRequested = true
		stopCause, stopSource = enrichment.deactivateLocked(nil)
	} else if navigation && enrichment.active {
		if result.Snapshot.Generation() != 0 {
			enrichment.baseGeneration = result.Snapshot.Generation()
		}
		enrichment.discardRequested = true
		stopCause, stopSource = enrichment.deactivateLocked(nil)
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if terminalEvent {
		cancelCause := stopCause
		if cancelCause == nil {
			cancelCause = context.Canceled
		}
		for _, cancelEvent := range otherEvents {
			cancelEvent(cancelCause)
		}
	}
	return stopCause, stopSource
}

func (enrichment *initialEnrichment) recordCallback(duration time.Duration) {
	if enrichment.metrics != nil {
		enrichment.metrics.recordCallback(duration)
	}
}
