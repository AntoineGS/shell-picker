package integration

import (
	"errors"
	"fmt"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

type dedicatedTraceSample struct {
	StartupDuration    time.Duration
	EnrichmentDuration *time.Duration
	LifecycleDuration  time.Duration
}

func parseDedicatedTraceSample(events []traceEvent, requireEnrichment bool) (dedicatedTraceSample, error) {
	if len(events) == 0 {
		return dedicatedTraceSample{}, errors.New("dedicated trace is empty")
	}
	sessionID := events[0].Session
	if sessionID == "" {
		return dedicatedTraceSample{}, errors.New("dedicated trace has no session")
	}
	var started, fzfStarted, closed time.Time
	enrichments := make([]struct {
		event traceEvent
		time  time.Time
	}, 0, 1)
	validationNow := time.Now()
	var previous time.Time
	for _, event := range events {
		if err := integrationpkg.ValidateTraceRecordAt(event, validationNow); err != nil {
			return dedicatedTraceSample{}, err
		}
		if event.Session != sessionID {
			return dedicatedTraceSample{}, errors.New("dedicated trace contains multiple sessions")
		}
		if event.Event == "trace.error" {
			return dedicatedTraceSample{}, fmt.Errorf("dedicated trace error: %s", event.Outcome)
		}
		stamp, err := parseDedicatedTraceTime(event)
		if err != nil {
			return dedicatedTraceSample{}, err
		}
		if !previous.IsZero() && stamp.Before(previous) {
			return dedicatedTraceSample{}, errors.New("dedicated trace timestamps decrease")
		}
		previous = stamp
		switch event.Event {
		case "session.start":
			if !started.IsZero() {
				return dedicatedTraceSample{}, errors.New("dedicated trace has duplicate session.start")
			}
			started = stamp
			if requireEnrichment && event.Outcome != "cd" {
				return dedicatedTraceSample{}, errors.New("dedicated trace source is not a CD session")
			}
		case "fzf.start":
			if !fzfStarted.IsZero() {
				return dedicatedTraceSample{}, errors.New("dedicated trace has duplicate fzf.start")
			}
			if event.Outcome != "ok" {
				return dedicatedTraceSample{}, errors.New("dedicated trace fzf.start is not ok")
			}
			fzfStarted = stamp
		case "session.close":
			if !closed.IsZero() {
				return dedicatedTraceSample{}, errors.New("dedicated trace has duplicate session.close")
			}
			closed = stamp
		case "zoxide.enrichment":
			enrichments = append(enrichments, struct {
				event traceEvent
				time  time.Time
			}{event: event, time: stamp})
		}
	}
	if started.IsZero() || fzfStarted.IsZero() || closed.IsZero() {
		return dedicatedTraceSample{}, errors.New("dedicated trace is missing a session marker")
	}
	if fzfStarted.Before(started) || closed.Before(started) || closed.Before(fzfStarted) {
		return dedicatedTraceSample{}, errors.New("dedicated trace timestamps reverse lifecycle")
	}
	for _, event := range events {
		stamp, err := parseDedicatedTraceTime(event)
		if err != nil {
			return dedicatedTraceSample{}, err
		}
		if stamp.After(closed) && event.Event != "session.close" {
			return dedicatedTraceSample{}, errors.New("dedicated trace event occurs after session.close")
		}
	}
	if requireEnrichment && len(enrichments) != 1 {
		return dedicatedTraceSample{}, fmt.Errorf("dedicated trace enrichment terminals=%d", len(enrichments))
	}
	if !requireEnrichment && len(enrichments) > 1 {
		return dedicatedTraceSample{}, fmt.Errorf("dedicated trace enrichment terminals=%d", len(enrichments))
	}
	measurement := dedicatedTraceSample{StartupDuration: fzfStarted.Sub(started), LifecycleDuration: closed.Sub(started)}
	if len(enrichments) == 1 {
		terminal := enrichments[0]
		if err := validateDedicatedEnrichmentTerminal(terminal.event); err != nil {
			return dedicatedTraceSample{}, err
		}
		if terminal.time.Before(started) || closed.Before(terminal.time) {
			return dedicatedTraceSample{}, errors.New("dedicated trace enrichment timestamp is outside session")
		}
		duration := terminal.time.Sub(started)
		measurement.EnrichmentDuration = &duration
	}
	return measurement, nil
}

func parseDedicatedTraceTime(event traceEvent) (time.Time, error) {
	if event.Time == "" {
		return time.Time{}, fmt.Errorf("dedicated trace event %s has no timestamp", event.Event)
	}
	if event.Time[len(event.Time)-1] != 'Z' {
		return time.Time{}, fmt.Errorf("dedicated trace event %s is not UTC", event.Event)
	}
	stamp, err := time.Parse(time.RFC3339Nano, event.Time)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse dedicated trace event %s: %w", event.Event, err)
	}
	return stamp, nil
}

func validateDedicatedEnrichmentTerminal(event traceEvent) error {
	if event.Generation != 1 {
		return fmt.Errorf("dedicated trace enrichment generation=%d; want initial generation 1", event.Generation)
	}
	if event.Outcome != "published" && event.Outcome != "discarded" && event.Outcome != "failed" {
		return fmt.Errorf("dedicated trace enrichment outcome %q is not terminal", event.Outcome)
	}
	switch event.ZoxideOutcome {
	case "ok", "missing", "process-error", "malformed", "timeout", "cancelled", "cached":
	default:
		return fmt.Errorf("dedicated trace enrichment zoxide outcome %q is not terminal", event.ZoxideOutcome)
	}
	if event.ZoxideAttempts <= 0 || event.ZoxideStarts < 0 || event.ZoxideExits < 0 || event.ZoxideProcesses < 0 ||
		event.ZoxideLive < 0 || event.ZoxideMaxLive < 0 || event.ZoxideStarts > event.ZoxideAttempts ||
		event.ZoxideExits != event.ZoxideStarts || event.ZoxideProcesses != event.ZoxideStarts || event.ZoxideLive != 0 ||
		event.ZoxideMaxLive > event.ZoxideStarts || (event.ZoxideStarts > 0 && event.ZoxideMaxLive == 0) {
		return errors.New("dedicated trace enrichment has invalid process counters")
	}
	return nil
}

func traceBenchmarkCounters(events []traceEvent, generation uint64) (integrationpkg.BenchmarkCounters, error) {
	counters := integrationpkg.BenchmarkCounters{}
	foundGeneration := false
	if generation == 0 {
		var terminals []traceEvent
		for _, event := range events {
			if event.Event == "zoxide.enrichment" {
				terminals = append(terminals, event)
			}
		}
		if len(terminals) != 1 {
			return integrationpkg.BenchmarkCounters{}, fmt.Errorf("measured zoxide enrichment terminals=%d", len(terminals))
		}
		if err := validateDedicatedEnrichmentTerminal(terminals[0]); err != nil {
			return integrationpkg.BenchmarkCounters{}, err
		}
		counters.ZoxideAttempts = terminals[0].ZoxideAttempts
		counters.ZoxideStarts = terminals[0].ZoxideStarts
		counters.ZoxideExits = terminals[0].ZoxideExits
		counters.ZoxideProcesses = terminals[0].ZoxideProcesses
		counters.ZoxideMaxLive = terminals[0].ZoxideMaxLive
		foundGeneration = true
	}
	for _, event := range events {
		if generation == 0 {
			if event.Event == "preview.finished" {
				counters.PreviewStarts += event.ChildStarts
				counters.PreviewMaxLive = max(counters.PreviewMaxLive, event.MaxLiveChildren)
			}
			continue
		}
		if event.Event == "generation.publish" {
			if event.Generation != generation {
				continue
			}
			if foundGeneration {
				return integrationpkg.BenchmarkCounters{}, errors.New("duplicate measured generation")
			}
			foundGeneration = true
			if event.ZoxideOutcome != "not-run" {
				return integrationpkg.BenchmarkCounters{}, errors.New("measured navigation generation ran zoxide")
			}
			if event.ZoxideAttempts != 0 || event.ZoxideStarts != 0 || event.ZoxideExits != 0 ||
				event.ZoxideProcesses != 0 || event.ZoxideLive != 0 || event.ZoxideMaxLive != 0 {
				return integrationpkg.BenchmarkCounters{}, errors.New("measured navigation generation has zoxide counters")
			}
		}
		if event.Event == "preview.finished" {
			counters.PreviewStarts += event.ChildStarts
			counters.PreviewMaxLive = max(counters.PreviewMaxLive, event.MaxLiveChildren)
		}
	}
	if !foundGeneration {
		return integrationpkg.BenchmarkCounters{}, errors.New("missing measured navigation generation")
	}
	return counters, nil
}
