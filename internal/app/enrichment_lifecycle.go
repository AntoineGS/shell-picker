package app

import (
	"context"
	"errors"
	"time"
)

// Start is idempotent. A coordinator created by newInitialEnrichment is
// already started; the method exists so lifecycle wiring can call Start at its
// ownership boundary without risking a second source goroutine.
func (enrichment *initialEnrichment) Start() error {
	if enrichment == nil {
		return errInitialEnrichmentNilSource
	}
	enrichment.startOnce.Do(func() {
		go enrichment.run()
	})
	return nil
}

func (enrichment *initialEnrichment) run() {
	defer func() {
		if enrichment.parentStop != nil {
			enrichment.parentStop()
		}
		enrichment.emitSourceTerminal()
		close(enrichment.done)
	}()

	sourceStarted := time.Now()
	result, sourceErr := enrichment.builder.LoadInitialZoxide(enrichment.ctx)
	if result.Metrics.ZoxideDuration == 0 {
		result.Metrics.ZoxideDuration = time.Since(sourceStarted)
		if result.Metrics.ZoxideDuration == 0 {
			result.Metrics.ZoxideDuration = time.Nanosecond
		}
	}
	enrichment.setSourceResult(result, sourceErr)
	if cause := context.Cause(enrichment.parent); cause != nil {
		if sourceErr != nil {
			enrichment.finishSource(joinLifecycleErrors(sourceErr, cause))
		} else {
			enrichment.Stop(cause)
		}
		return
	}
	if !enrichment.waitForActivation() {
		enrichment.retainInterruptedSourceError(sourceErr)
		return
	}
	if !enrichment.isActive() {
		enrichment.retainInterruptedSourceError(sourceErr)
		return
	}
	if cause := context.Cause(enrichment.parent); cause != nil {
		enrichment.Stop(cause)
		return
	}
	if sourceErr != nil || result.Discarded {
		// A terminal source result has nothing to commit, but it must still wait
		// for local activation so the startup stream remains usable until the
		// local generation is published. A non-nil error is authoritative.
		enrichment.finishSource(sourceErr)
		return
	}
	enrichment.commit(result)
}

func (enrichment *initialEnrichment) retainInterruptedSourceError(sourceErr error) {
	if sourceErr == nil {
		return
	}
	if errors.Is(sourceErr, context.Canceled) && context.Cause(enrichment.parent) == nil {
		return
	}
	enrichment.finishSource(sourceErr)
}

func (enrichment *initialEnrichment) waitForActivation() bool {
	select {
	case <-enrichment.ready:
		return true
	case <-enrichment.ctx.Done():
		// Navigation may cancel the enrichment owner while the source is still
		// waiting for activation. That is a discard, not a terminal session stop.
		if cause := context.Cause(enrichment.parent); cause != nil {
			enrichment.Stop(cause)
		}
		return false
	}
}

// Activate publishes the exact nonzero local generation that is eligible for
// enrichment. It can be accepted once only.
func (enrichment *initialEnrichment) Activate(baseGeneration uint64) error {
	if enrichment == nil {
		return errInitialEnrichmentNilSource
	}
	if baseGeneration == 0 {
		return errInitialEnrichmentZeroBase
	}
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	if enrichment.activated {
		return errInitialEnrichmentActivated
	}
	if !enrichment.active {
		if enrichment.terminalErr != nil {
			return enrichment.terminalErr
		}
		return errInitialEnrichmentInactive
	}
	if cause := context.Cause(enrichment.parent); cause != nil {
		return cause
	}
	enrichment.activated = true
	enrichment.initialGeneration = baseGeneration
	enrichment.baseGeneration = baseGeneration
	close(enrichment.ready)
	return nil
}

func (enrichment *initialEnrichment) isActive() bool {
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	return enrichment.active
}

func (enrichment *initialEnrichment) finishSource(sourceErr error) {
	enrichment.gate.Lock()
	cause, changed := enrichment.deactivateLocked(sourceErr)
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if changed {
		enrichment.finish(cause)
	}
}

// Stop terminalizes the coordinator, closes the stream, and cancels the
// source. It never waits for the source goroutine; callers that need a joined
// lifecycle use Wait.
func (enrichment *initialEnrichment) Stop(cause error) error {
	if enrichment == nil {
		return nil
	}
	enrichment.gate.Lock()
	enrichment.terminal = true
	enrichment.discardRequested = context.Cause(enrichment.parent) == nil
	effective, _ := enrichment.deactivateLocked(cause)
	enrichment.pendingEvents = make(map[uint64]*pendingEvent)
	enrichment.inFlight = len(enrichment.eventCancels)
	cancels := make([]context.CancelCauseFunc, 0, len(enrichment.eventCancels))
	for _, cancelEvent := range enrichment.eventCancels {
		cancels = append(cancels, cancelEvent)
	}
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	cancelCause := effective
	if cancelCause == nil {
		cancelCause = context.Canceled
	}
	for _, cancelEvent := range cancels {
		cancelEvent(cancelCause)
	}
	enrichment.finish(effective)
	return effective
}

// Wait joins the source goroutine and returns only the authoritative lifecycle
// error. Soft source failures are deliberately not retained here.
func (enrichment *initialEnrichment) Wait() error {
	if enrichment == nil {
		return errInitialEnrichmentNilSource
	}
	<-enrichment.done
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	if !enrichment.terminalFinalized {
		enrichment.terminalErr = joinLifecycleErrors(enrichment.terminalErr, context.Cause(enrichment.parent))
		enrichment.terminalFinalized = true
	}
	return enrichment.terminalErr
}

func (enrichment *initialEnrichment) deactivateLocked(cause error) (error, bool) {
	cause = joinLifecycleErrors(cause, context.Cause(enrichment.parent))
	if !enrichment.active {
		if cause != nil && !enrichment.terminalFinalized {
			enrichment.terminalErr = joinLifecycleErrors(enrichment.terminalErr, cause)
		}
		return enrichment.terminalErr, false
	}
	enrichment.active = false
	enrichment.terminalErr = joinLifecycleErrors(enrichment.terminalErr, cause)
	return enrichment.terminalErr, true
}

func (enrichment *initialEnrichment) signalLocked() {
	if enrichment.stateChanged == nil {
		enrichment.stateChanged = make(chan struct{})
		return
	}
	close(enrichment.stateChanged)
	enrichment.stateChanged = make(chan struct{})
}

func joinLifecycleErrors(existing, incoming error) error {
	if incoming == nil {
		return existing
	}
	if existing == nil {
		return incoming
	}
	if errors.Is(existing, incoming) {
		return existing
	}
	if errors.Is(incoming, existing) {
		return incoming
	}
	return errors.Join(existing, incoming)
}
