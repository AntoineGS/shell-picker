package app

import (
	"fmt"
	"io"
	"sync"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

type pickerTrace struct {
	trace      *integrationpkg.Trace
	sink       io.WriteCloser
	diagnostic io.Writer
	once       sync.Once
}

func openPickerTrace(path string, sessionID [16]byte, diagnostic io.Writer) (*pickerTrace, error) {
	if path == "" {
		return nil, nil
	}
	sink, err := openTraceSink(path)
	if err != nil {
		return nil, fmt.Errorf("open picker trace: %w", err)
	}
	return &pickerTrace{trace: integrationpkg.NewTrace(sink, sessionID), sink: sink, diagnostic: diagnostic}, nil
}

func (trace *pickerTrace) event(event integrationpkg.TraceEvent) {
	if trace == nil {
		return
	}
	if err := trace.trace.Event(event); err != nil {
		trace.disabledDiagnostic()
	}
}

func (trace *pickerTrace) finish(outcome protocol.Outcome, primary error) (protocol.Outcome, error) {
	if trace == nil {
		return outcome, primary
	}
	status := "error"
	if primary == nil {
		status = string(outcome.Status)
	}
	trace.event(integrationpkg.TraceEvent{Name: "session.close", Outcome: status})
	if trace.trace != nil {
		if err := trace.trace.Close(); err != nil {
			trace.disabledDiagnostic()
		}
	} else if trace.sink != nil {
		if err := trace.sink.Close(); err != nil {
			trace.disabledDiagnostic()
		}
	}
	return outcome, primary
}

func (trace *pickerTrace) disabledDiagnostic() {
	trace.once.Do(func() {
		if trace.diagnostic != nil {
			_, _ = fmt.Fprintln(trace.diagnostic, "shell-picker: trace disabled")
		}
	})
}

func traceInitialTransition(trace *pickerTrace, policy candidate.ZoxidePolicy, picker protocol.Picker, result session.TransitionResult, path []byte) {
	metrics := result.Metrics
	sources := metrics.Sources
	zeroZoxideMetrics(&sources)
	if picker == protocol.PickerCD {
		sources.ZoxideOutcome = "pending"
	} else {
		sources.ZoxideOutcome = "not-run"
	}
	traceGenerationTransition(trace, policy, result, path, sources)
}

func traceTransition(trace *pickerTrace, policy candidate.ZoxidePolicy, result session.TransitionResult, path []byte) {
	sources := result.Metrics.Sources
	zeroZoxideMetrics(&sources)
	sources.ZoxideOutcome = "not-run"
	traceGenerationTransition(trace, policy, result, path, sources)
}

func zeroZoxideMetrics(source *candidate.SourceMetrics) {
	source.ZoxideAttempts = 0
	source.ZoxideStarts = 0
	source.ZoxideExits = 0
	source.ZoxideProcesses = 0
	source.ZoxideLive = 0
	source.ZoxideMaxLive = 0
	source.ZoxideDuration = 0
}

func traceGenerationTransition(trace *pickerTrace, policy candidate.ZoxidePolicy, result session.TransitionResult, path []byte, sources candidate.SourceMetrics) {
	trace.event(integrationpkg.TraceEvent{Name: "generation.publish", Generation: result.Snapshot.Generation(),
		CandidateCount: len(result.Snapshot.Records()), Outcome: "ok", Path: path, ZoxidePolicy: tracePolicy(policy),
		ZoxideAttempts: sources.ZoxideAttempts, ZoxideStarts: sources.ZoxideStarts,
		ZoxideExits: sources.ZoxideExits, ZoxideProcesses: sources.ZoxideProcesses,
		ZoxideLive: sources.ZoxideLive, ZoxideMaxLive: sources.ZoxideMaxLive,
		ActorQueueWait: result.Metrics.QueueWait, LocalDuration: sources.LocalDuration,
		ZoxideDuration: sources.ZoxideDuration, ZoxideOutcome: sources.ZoxideOutcome,
		TransformDuration: result.Metrics.TransformDuration})
}

func traceZoxideEnrichment(trace *pickerTrace, policy candidate.ZoxidePolicy, generation uint64, lifecycle string, candidateCount int, source candidate.SourceMetrics) {
	trace.event(integrationpkg.TraceEvent{Name: "zoxide.enrichment", Generation: generation,
		CandidateCount: candidateCount, Outcome: lifecycle, ZoxidePolicy: tracePolicy(policy),
		ZoxideAttempts: source.ZoxideAttempts, ZoxideStarts: source.ZoxideStarts,
		ZoxideExits: source.ZoxideExits, ZoxideProcesses: source.ZoxideProcesses,
		ZoxideLive: source.ZoxideLive, ZoxideMaxLive: source.ZoxideMaxLive,
		ZoxideDuration: source.ZoxideDuration, ZoxideOutcome: source.ZoxideOutcome})
}

func tracePolicy(policy candidate.ZoxidePolicy) string {
	if policy == 0 {
		return candidate.ZoxideCached.String()
	}
	return policy.String()
}
