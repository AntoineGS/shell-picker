package app

import (
	"context"
	"errors"

	"github.com/AntoineGS/shell-picker/internal/candidate"
)

func (enrichment *initialEnrichment) finish(cause error) {
	enrichment.closeOnce.Do(func() {
		if cause != nil {
			_ = enrichment.input.CloseWithError(cause)
		} else {
			_ = enrichment.input.Close()
		}
	})
	enrichment.cancelOnce.Do(func() { enrichment.cancel(cause) })
}

func (enrichment *initialEnrichment) cancelSource(cause error) {
	enrichment.cancelOnce.Do(func() { enrichment.cancel(cause) })
}

func (enrichment *initialEnrichment) setSourceResult(result candidate.InitialZoxideResult, sourceErr error) {
	enrichment.gate.Lock()
	enrichment.sourceResult = result
	enrichment.sourceErr = sourceErr
	enrichment.gate.Unlock()
}

func (enrichment *initialEnrichment) setTraceDecision(outcome string, generation uint64, candidates int) {
	enrichment.gate.Lock()
	defer enrichment.gate.Unlock()
	if enrichment.traceOutcome != "" {
		return
	}
	if outcome != "published" {
		generation = 0
		candidates = 0
	}
	enrichment.traceOutcome = outcome
	enrichment.traceGeneration = generation
	enrichment.traceCandidates = candidates
}

func (enrichment *initialEnrichment) setBaseGeneration(generation uint64) {
	if generation == 0 {
		return
	}
	enrichment.gate.Lock()
	enrichment.baseGeneration = generation
	enrichment.gate.Unlock()
}

func (enrichment *initialEnrichment) emitSourceTerminal() {
	enrichment.traceOnce.Do(func() {
		enrichment.gate.Lock()
		result := enrichment.sourceResult
		sourceErr := enrichment.sourceErr
		lifecycle := enrichment.traceOutcome
		generation := enrichment.traceGeneration
		candidateCount := enrichment.traceCandidates
		initialGeneration := enrichment.initialGeneration
		discardRequested := enrichment.discardRequested
		enrichment.gate.Unlock()

		source := normalizeSourceMetrics(result.Metrics, sourceErr, enrichment.parent)
		if lifecycle == "" {
			if context.Cause(enrichment.parent) != nil {
				lifecycle = "failed"
			} else {
				switch {
				case sourceErr != nil && source.ZoxideOutcome != "cancelled" && source.ZoxideOutcome != "timeout":
					lifecycle = "failed"
				case source.ZoxideOutcome == "missing" || source.ZoxideOutcome == "process-error" ||
					source.ZoxideOutcome == "malformed" || source.ZoxideOutcome == "timeout":
					lifecycle = "failed"
				case source.ZoxideOutcome == "cancelled" && !discardRequested:
					lifecycle = "failed"
				case result.Discarded && !discardRequested:
					lifecycle = "failed"
				case discardRequested:
					lifecycle = "discarded"
				default:
					lifecycle = "discarded"
				}
			}
		}
		if lifecycle != "published" {
			candidateCount = 0
			generation = initialGeneration
		}
		if generation == 0 {
			// The actor reserves generation one before the initial local build.
			// Keep the standalone source terminal valid even when that build never
			// reaches an actor snapshot.
			generation = 1
		}
		if enrichment.metrics != nil {
			enrichment.metrics.recordZoxideSource(source)
		}
		traceZoxideEnrichment(enrichment.trace, enrichment.policy, generation, lifecycle, candidateCount, source)
	})
}

func normalizeSourceMetrics(source candidate.SourceMetrics, sourceErr error, parent context.Context) candidate.SourceMetrics {
	parentCause := context.Cause(parent)
	if sourceErr != nil {
		switch {
		case parentCause != nil && errors.Is(sourceErr, parentCause):
			source.ZoxideOutcome = "cancelled"
		case errors.Is(sourceErr, context.DeadlineExceeded):
			source.ZoxideOutcome = "timeout"
		case errors.Is(sourceErr, context.Canceled):
			source.ZoxideOutcome = "cancelled"
		case source.ZoxideOutcome == "" || source.ZoxideOutcome == "ok" || source.ZoxideOutcome == "cached":
			source.ZoxideOutcome = "process-error"
		}
	}
	if source.ZoxideOutcome == "" {
		source.ZoxideOutcome = "process-error"
	}
	if source.ZoxideOutcome == "pending" || source.ZoxideOutcome == "not-run" {
		source.ZoxideOutcome = "process-error"
	}
	return source
}
