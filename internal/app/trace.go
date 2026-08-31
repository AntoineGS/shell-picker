package app

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzfsidecar"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
	"github.com/AntoineGS/shell-picker/internal/session"
)

var errPickerTraceClosed = errors.New("picker trace is closed")

const (
	callbackTracePathEnvironment    = "SHELL_PICKER_TRACE_PATH"
	callbackTraceSessionEnvironment = "SHELL_PICKER_TRACE_SESSION"
)

type pickerTrace struct {
	trace      *integrationpkg.Trace
	sink       io.WriteCloser
	diagnostic io.Writer
	once       sync.Once
	finishOnce sync.Once
	mu         sync.Mutex
	closed     bool
	closeErr   error
}

func openPickerTrace(path string, sessionID [16]byte, diagnostic io.Writer) (*pickerTrace, error) {
	if path == "" {
		return nil, nil
	}
	sink, err := openTraceSink(path, sessionID)
	if err != nil {
		return nil, fmt.Errorf("open picker trace: %w", err)
	}
	return &pickerTrace{trace: integrationpkg.NewTrace(sink, sessionID), sink: sink, diagnostic: diagnostic}, nil
}

func openCallbackTrace(getenv func(string) string) (*pickerTrace, error) {
	if getenv == nil {
		return nil, nil
	}
	path := getenv(callbackTracePathEnvironment)
	session := getenv(callbackTraceSessionEnvironment)
	if path == "" || session == "" {
		return nil, nil
	}
	if _, err := integrationpkg.NewTraceWithRedactedSession(io.Discard, session); err != nil {
		return nil, nil
	}
	sink, err := openTraceSinkWithExpectedSession(path, session)
	if err != nil {
		return nil, nil
	}
	trace, err := integrationpkg.NewTraceWithRedactedSession(sink, session)
	if err != nil {
		_ = sink.Close()
		return nil, nil
	}
	return &pickerTrace{trace: trace, sink: sink}, nil
}

func (trace *pickerTrace) event(event integrationpkg.TraceEvent) error {
	if trace == nil {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.closed {
		return errPickerTraceClosed
	}
	if trace.trace == nil {
		return errors.New("picker trace has no trace writer")
	}
	if err := trace.trace.Event(event); err != nil {
		trace.disabledDiagnostic()
		return err
	}
	return nil
}

func (trace *pickerTrace) finish(outcome protocol.Outcome, primary error) (protocol.Outcome, error) {
	if trace == nil {
		return outcome, primary
	}
	trace.finishOnce.Do(func() {
		status := "error"
		if primary == nil {
			status = string(outcome.Status)
		}
		_ = trace.event(integrationpkg.TraceEvent{Name: "session.close", Outcome: status})
		_ = trace.close()
	})
	return outcome, primary
}

func (trace *pickerTrace) close() error {
	if trace == nil {
		return nil
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.closed {
		return trace.closeErr
	}
	trace.closed = true
	if trace.trace != nil {
		trace.closeErr = trace.trace.Close()
	} else if trace.sink != nil {
		trace.closeErr = trace.sink.Close()
	}
	if trace.closeErr != nil {
		trace.disabledDiagnostic()
	}
	return trace.closeErr
}

func (trace *pickerTrace) disabledDiagnostic() {
	trace.once.Do(func() {
		if trace.diagnostic != nil {
			_, _ = fmt.Fprintln(trace.diagnostic, "shell-picker: trace disabled")
		}
	})
}

func newSidecarTraceObserver(trace *pickerTrace) fzfsidecar.Observer {
	if trace == nil {
		return nil
	}
	return fzfsidecar.ObserverFunc(func(event fzfsidecar.ObserverEvent) {
		name, outcome, ok := sidecarTraceCategory(event)
		if !ok {
			return
		}
		trace.event(integrationpkg.TraceEvent{Name: name, Outcome: outcome,
			SidecarAttempt: event.Attempt, LocalDuration: event.Duration})
	})
}

func sidecarTraceCategory(event fzfsidecar.ObserverEvent) (name, outcome string, ok bool) {
	switch event.Kind {
	case fzfsidecar.ObserverGetSuccess:
		return "sidecar.get", "success", true
	case fzfsidecar.ObserverGetTransient:
		return "sidecar.get", "transient", true
	case fzfsidecar.ObserverGetTerminal:
		return "sidecar.get", "terminal", true
	case fzfsidecar.ObserverPostSuccess:
		return "sidecar.post", "success", true
	case fzfsidecar.ObserverPostTransient:
		return "sidecar.post", "transient", true
	case fzfsidecar.ObserverPostTerminal:
		return "sidecar.post", "terminal", true
	case fzfsidecar.ObserverStop:
		switch event.StopReason {
		case fzfsidecar.ObserverStopContextCanceled:
			return "sidecar.stop", "context-canceled", true
		case fzfsidecar.ObserverStopReadinessTimeout:
			return "sidecar.stop", "readiness-timeout", true
		case fzfsidecar.ObserverStopTransientWindow:
			return "sidecar.stop", "transient-window", true
		case fzfsidecar.ObserverStopTerminal:
			return "sidecar.stop", "terminal", true
		case fzfsidecar.ObserverStopRequested:
			return "sidecar.stop", "requested", true
		}
	}
	return "", "", false
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
		CandidateCount: result.Snapshot.RecordCount(), Outcome: "ok", Path: path, ZoxidePolicy: tracePolicy(policy),
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
