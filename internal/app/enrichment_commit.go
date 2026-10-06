package app

import (
	"context"
	"errors"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/session"
)

func (enrichment *initialEnrichment) commit(source candidate.InitialZoxideResult) {
	if !enrichment.beginCommit() {
		return
	}

	enrichment.gate.Lock()
	baseGeneration := enrichment.baseGeneration
	enrichment.gate.Unlock()
	base, err := enrichment.actor.Snapshot(enrichment.ctx, baseGeneration)
	if err != nil || base.Generation() != baseGeneration {
		if err != nil && !errors.Is(err, session.ErrTransitionPending) && !errors.Is(err, context.Canceled) {
			if current, currentErr := enrichment.actor.Current(context.Background()); currentErr == nil && current.Generation() > baseGeneration {
				enrichment.setBaseGeneration(current.Generation())
			}
		}
		terminal := error(nil)
		if cause := context.Cause(enrichment.parent); cause != nil {
			terminal = cause
		}
		enrichment.finishCommit(nil, terminal)
		return
	}
	sourceRecords := append([]candidate.Record(nil), source.Records...)
	candidate.CompactHomeDisplays(sourceRecords, enrichment.home)
	merged, admitted := candidate.MergeNewRecords(base.Records(), sourceRecords)
	if len(admitted) == 0 {
		enrichment.finishCommit(nil, nil)
		return
	}

	// Actor.Enrich has published the generation before the framed bytes are
	// made visible to fzf. Append copies the frame into the session-owned
	// memory stream and cannot retry after a close.
	var result session.TransitionResult
	appendErr := enrichment.input.AppendAfter(frameCandidateRecords(admitted), func() error {
		enrichment.gate.Lock()
		active := enrichment.active && !enrichment.terminal
		enrichment.gate.Unlock()
		if !active {
			return fzf.ErrInputClosed
		}
		var err error
		result, err = enrichment.actor.Enrich(enrichment.ctx, baseGeneration, merged, source.Metrics)
		return err
	})
	if appendErr != nil {
		terminal := error(nil)
		switch {
		case errors.Is(appendErr, fzf.ErrInputClosed),
			errors.Is(appendErr, context.Canceled),
			errors.Is(appendErr, context.DeadlineExceeded):
			// Normal fzf exit and coordinator cancellation discard the
			// transaction without becoming picker errors.
		default:
			terminal = appendErr
		}
		if cause := context.Cause(enrichment.parent); cause != nil {
			terminal = joinLifecycleErrors(terminal, cause)
		}
		enrichment.finishCommit(nil, terminal)
		return
	}
	enrichment.finishCommit(&result, nil)
}

func (enrichment *initialEnrichment) beginCommit() bool {
	for {
		enrichment.gate.Lock()
		if !enrichment.active || enrichment.terminal {
			enrichment.committing = false
			enrichment.signalLocked()
			enrichment.gate.Unlock()
			return false
		}
		if !enrichment.committing && enrichment.inFlight == 0 {
			enrichment.committing = true
		}
		if enrichment.inFlight == 0 {
			enrichment.gate.Unlock()
			return true
		}
		changed := enrichment.stateChanged
		enrichment.gate.Unlock()

		select {
		case <-changed:
		case <-enrichment.ctx.Done():
			cause := context.Cause(enrichment.parent)
			if cause != nil {
				_ = enrichment.Stop(cause)
				continue
			}
			// A navigation canceled this owner while an event was in flight.
			// Release the commit gate without terminalizing later events.
			enrichment.gate.Lock()
			enrichment.committing = false
			enrichment.signalLocked()
			enrichment.gate.Unlock()
			return false
		}
	}
}

func (enrichment *initialEnrichment) finishCommit(result *session.TransitionResult, terminal error) {
	if result != nil {
		enrichment.recordTransition(*result)
		enrichment.setTraceDecision("published", result.Snapshot.Generation(), result.Snapshot.RecordCount())
	} else if terminal == nil {
		enrichment.setTraceDecision("discarded", 0, 0)
	} else if context.Cause(enrichment.parent) != nil {
		enrichment.setTraceDecision("failed", 0, 0)
	} else if !errors.Is(terminal, fzf.ErrInputClosed) && !errors.Is(terminal, context.Canceled) &&
		!errors.Is(terminal, context.DeadlineExceeded) {
		enrichment.setTraceDecision("failed", 0, 0)
	} else {
		enrichment.setTraceDecision("discarded", 0, 0)
	}
	enrichment.gate.Lock()
	if result != nil {
		enrichment.baseGeneration = result.Snapshot.Generation()
	}
	cause, changed := enrichment.deactivateLocked(terminal)
	enrichment.committing = false
	enrichment.signalLocked()
	enrichment.gate.Unlock()
	if changed || cause != nil {
		enrichment.finish(cause)
	}
}

func (enrichment *initialEnrichment) recordTransition(result session.TransitionResult) {
	if enrichment.metrics != nil {
		enrichment.metrics.recordTransition(result)
	}
}
