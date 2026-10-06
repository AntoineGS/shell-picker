package app

import (
	"context"
	"time"

	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

// HandleEvent reserves an event under the gate, then performs session work
// without holding it. A successful event keeps its reservation until the
// callback action is acknowledged and, for reload effects, the exact
// generation has been copied by LoadGeneration.
func (enrichment *initialEnrichment) HandleEvent(ctx context.Context, event protocol.Event) (sessionipc.EventResult, error) {
	if enrichment == nil {
		return sessionipc.EventResult{}, errInitialEnrichmentNilSource
	}
	started := time.Now()
	eventCtx, eventID, cancelEvent, reserveErr := enrichment.reserveEvent(ctx)
	if reserveErr != nil {
		return sessionipc.EventResult{}, reserveErr
	}
	result, err := session.Handle(eventCtx, enrichment.actor, event)
	if err == nil {
		result = enrichment.suppressActiveRestore(event, result)
	}
	stopCause, stopSource := enrichment.resolveEvent(event, eventID, result, err)
	if err == nil && stopCause != nil && !stopSource {
		err = stopCause
	}
	cancelEvent(nil)

	duration := time.Since(started)
	if err == nil {
		if enrichment.metrics != nil {
			enrichment.metrics.recordTransition(result)
		}
		if result.Effect.ReloadGeneration != 0 {
			enrichment.setBaseGeneration(result.Snapshot.Generation())
			state := result.Snapshot.State()
			traceTransition(enrichment.trace, enrichment.policy, result, state.Location.Path)
		}
	}
	enrichment.recordCallback(duration)
	if stopSource {
		// Cancel stale enrichment immediately. Reload action finalization closes
		// the initial stream; terminal actions leave closure to process exit.
		enrichment.cancelSource(stopCause)
	}
	if err != nil {
		return sessionipc.EventResult{}, err
	}
	return sessionipc.EventResult{Effect: result.Effect, EventID: eventID}, nil
}

func (enrichment *initialEnrichment) suppressActiveRestore(event protocol.Event, result session.TransitionResult) session.TransitionResult {
	if event.Opcode == protocol.OpRestoreView {
		return result
	}
	if result.Effect.RestoreGeneration == 0 || result.Effect.ReloadGeneration != 0 || result.Effect.Accept || result.Effect.Abort {
		return result
	}
	enrichment.gate.Lock()
	active := enrichment.active
	enrichment.gate.Unlock()
	if !active {
		return result
	}
	if event.Opcode == protocol.OpRestoreView {
		result.Effect = protocol.Effect{}
	} else {
		result.Effect.RestoreGeneration = 0
	}
	return result
}

// FinalizeEvent acknowledges exactly one coordinator event action. Reload and
// restore reservations remain held until the matching load is finalized.
func (enrichment *initialEnrichment) FinalizeEvent(_ context.Context, request sessionipc.EventFinalizeRequest) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	pending, ok := enrichment.pendingEvents[request.EventID]
	if !ok || pending.finalized {
		enrichment.gate.Unlock()
		return nil
	}
	if !request.Applied {
		enrichment.gate.Unlock()
		return enrichment.failCallbackApplication()
	}
	pending.finalized = true
	pending.applied = true
	closeInput := pending.closeInput
	if pending.generation == 0 {
		delete(enrichment.pendingEvents, request.EventID)
		if enrichment.inFlight > 0 {
			enrichment.inFlight--
		}
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if closeInput {
		// fzf waits for EOF on its initial reader before launching a reload.
		// The load uses its own callback stdout, not this initial stream. Keep
		// the event reservation until that exact load is finalized.
		_ = enrichment.input.Close()
	}
	return nil
}

func (enrichment *initialEnrichment) BeginLoad(request sessionipc.LoadRequest) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	if err := enrichment.validateLoadLocked(request); err != nil {
		enrichment.gate.Unlock()
		return err
	}
	pending := enrichment.pendingEvents[request.EventID]
	pending.loadRequested = true
	enrichment.gate.Unlock()
	return nil
}

func (enrichment *initialEnrichment) ValidateLoad(ctx context.Context, request sessionipc.LoadRequest) error {
	if enrichment == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		enrichment.gate.Lock()
		pending, ok := enrichment.pendingEvents[request.EventID]
		if !ok || request.EventID == 0 || request.Generation == 0 || pending.generation != request.Generation || pending.loadRequested {
			enrichment.gate.Unlock()
			return errInitialEnrichmentLoadReservation
		}
		if pending.finalized && pending.applied {
			enrichment.gate.Unlock()
			return nil
		}
		changed := enrichment.stateChanged
		enrichment.gate.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func (enrichment *initialEnrichment) validateLoadLocked(request sessionipc.LoadRequest) error {
	pending, ok := enrichment.pendingEvents[request.EventID]
	if !ok || request.EventID == 0 || request.Generation == 0 || pending.generation != request.Generation ||
		!pending.finalized || !pending.applied || pending.loadRequested {
		return errInitialEnrichmentLoadReservation
	}
	return nil
}

func (enrichment *initialEnrichment) FinalizeLoad(_ context.Context, request sessionipc.LoadFinalizeRequest) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	pending, ok := enrichment.pendingEvents[request.EventID]
	if !ok || !pending.finalized || !pending.applied {
		enrichment.gate.Unlock()
		return errInitialEnrichmentLoadReservation
	}
	if !request.Applied {
		enrichment.gate.Unlock()
		return enrichment.failCallbackApplication()
	}
	if !pending.loadRequested {
		enrichment.gate.Unlock()
		return errInitialEnrichmentLoadReservation
	}
	closeInput := pending.closeInput
	delete(enrichment.pendingEvents, request.EventID)
	if enrichment.inFlight > 0 {
		enrichment.inFlight--
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if closeInput {
		_ = enrichment.input.Close()
	}
	return nil
}

func (enrichment *initialEnrichment) failCallbackApplication() error {
	enrichment.gate.Lock()
	enrichment.terminal = true
	enrichment.discardRequested = true
	effective, _ := enrichment.deactivateLocked(errInitialEnrichmentCallbackApplication)
	enrichment.pendingEvents = make(map[uint64]*pendingEvent)
	enrichment.inFlight = len(enrichment.eventCancels)
	cancels := make([]context.CancelCauseFunc, 0, len(enrichment.eventCancels))
	for _, cancelEvent := range enrichment.eventCancels {
		cancels = append(cancels, cancelEvent)
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	for _, cancelEvent := range cancels {
		cancelEvent(effective)
	}
	enrichment.finish(effective)
	return effective
}
