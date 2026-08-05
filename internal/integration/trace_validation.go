package integration

import (
	"errors"
	"strings"
	"time"
)

func validateTraceEvent(event TraceEvent) error {
	return validateTraceEventWithSchema(event, traceSchemaAuthority(), time.Now())
}

func validateTraceEventWithSchema(event TraceEvent, schema traceSchemaRules, now time.Time) error {
	if !event.Timestamp.IsZero() && !now.IsZero() {
		if event.Timestamp.After(now.Add(schema.TimestampFutureLimit)) ||
			event.Timestamp.Before(now.Add(-schema.TimestampPastLimit)) {
			return errors.New("trace: invalid timestamp")
		}
	}
	if event.Name == "zoxide.enrichment" && event.Generation == 0 {
		return errors.New("trace: enrichment generation is required")
	}
	if event.Generation > 0 && event.Name != "generation.start" && event.Name != "generation.publish" &&
		event.Name != "generation.discard" && event.Name != "zoxide.enrichment" &&
		event.Name != "callback.load" && event.Name != "callback.load.start" {
		return errors.New("trace: generation is not valid for event")
	}
	if event.CandidateCount < schema.CandidateCountMin || event.CandidateCount > schema.CandidateCountMax ||
		event.CandidateCount != 0 && event.Name != "generation.publish" &&
			!(event.Name == "zoxide.enrichment" && event.Outcome == "published") {
		return errors.New("trace: invalid candidate count")
	}
	if event.Path != nil && event.Name != "generation.publish" {
		return errors.New("trace: path is not valid for event")
	}
	if event.Renderer != "" && event.Name != "preview.dispatch" && event.Name != "preview.finished" {
		if event.Name != "preview.cancel" && event.Name != "preview.exit" {
			return errors.New("trace: renderer is not valid for event")
		}
	}
	if isPreviewEvent(event.Name) && !validRendererWithSchema(event.Renderer, schema) {
		return errors.New("trace: invalid renderer")
	}
	if event.ChildStarts < schema.ChildStartsMin || event.ChildStarts > schema.ChildStartsMax ||
		event.MaxLiveChildren < schema.MaxLiveChildrenMin || event.MaxLiveChildren > schema.MaxLiveChildrenMax ||
		event.MaxLiveChildren > event.ChildStarts {
		return errors.New("trace: invalid preview child counters")
	}
	if event.ChildStarts != 0 || event.MaxLiveChildren != 0 {
		if event.Name != "preview.finished" && event.Name != "preview.exit" {
			return errors.New("trace: preview child counters are not valid for event")
		}
	}
	if event.Renderer == "native" && (event.ChildStarts != 0 || event.MaxLiveChildren != 0) {
		return errors.New("trace: native preview cannot have child counters")
	}
	if err := validateZoxideFields(event, schema); err != nil {
		return err
	}
	if err := validateSidecarFields(event, schema); err != nil {
		return err
	}
	if err := validateTimingFields(event, schema); err != nil {
		return err
	}
	if !validTraceOutcomeWithSchema(event.Name, event.Outcome, schema) {
		return errors.New("trace: invalid event or outcome")
	}
	return nil
}

func isPreviewEvent(name string) bool {
	return name == "preview.dispatch" || name == "preview.finished" || name == "preview.cancel" || name == "preview.exit"
}

func validateZoxideFields(event TraceEvent, schema traceSchemaRules) error {
	if event.ZoxideAttempts < schema.ZoxideCounterMin || event.ZoxideStarts < schema.ZoxideCounterMin ||
		event.ZoxideExits < schema.ZoxideCounterMin || event.ZoxideProcesses < schema.ZoxideCounterMin ||
		event.ZoxideLive < schema.ZoxideCounterMin || event.ZoxideMaxLive < schema.ZoxideCounterMin ||
		event.ZoxideAttempts > schema.ZoxideAttemptsMax ||
		event.ZoxideStarts > event.ZoxideAttempts || event.ZoxideExits != event.ZoxideStarts ||
		event.ZoxideProcesses != event.ZoxideStarts || event.ZoxideLive != schema.ZoxideLiveRequired ||
		event.ZoxideMaxLive > event.ZoxideStarts {
		return errors.New("trace: invalid zoxide counters")
	}
	hasFields := event.ZoxidePolicy != "" || event.ZoxideAttempts != 0 || event.ZoxideStarts != 0 || event.ZoxideExits != 0 ||
		event.ZoxideProcesses != 0 || event.ZoxideLive != 0 ||
		event.ZoxideMaxLive != 0 || event.ZoxideDuration != 0 || event.ZoxideOutcome != ""
	if hasFields && event.Name != "generation.publish" && event.Name != "generation.discard" && event.Name != "zoxide.enrichment" {
		return errors.New("trace: zoxide fields are not valid for event")
	}
	if hasFields && (event.ZoxidePolicy == "" || event.ZoxideOutcome == "") {
		return errors.New("trace: incomplete zoxide fields")
	}
	if event.Name == "zoxide.enrichment" && (event.ZoxidePolicy == "" || event.ZoxideOutcome == "") {
		return errors.New("trace: enrichment requires zoxide fields")
	}
	if event.ZoxidePolicy != "" && !schemaContains(schema.ZoxidePolicies, event.ZoxidePolicy) {
		return errors.New("trace: invalid zoxide policy")
	}
	if event.ZoxideOutcome != "" && !schemaContains(schema.ZoxideOutcomes, event.ZoxideOutcome) {
		return errors.New("trace: invalid zoxide outcome")
	}
	if event.ZoxideOutcome == "pending" {
		if event.Name != "generation.publish" || event.ZoxideAttempts != 0 || event.ZoxideStarts != 0 ||
			event.ZoxideExits != 0 || event.ZoxideProcesses != 0 || event.ZoxideLive != 0 ||
			event.ZoxideMaxLive != 0 || event.ZoxideDuration != 0 {
			return errors.New("trace: pending zoxide source has terminal fields")
		}
	}
	if event.Name == "zoxide.enrichment" && (event.ZoxideOutcome == "pending" || event.ZoxideOutcome == "not-run") {
		return errors.New("trace: enrichment requires terminal zoxide outcome")
	}
	return nil
}

func validateSidecarFields(event TraceEvent, schema traceSchemaRules) error {
	if event.SidecarAttempt > schema.SidecarAttemptMax {
		return errors.New("trace: invalid sidecar attempt")
	}
	isSidecar := event.Name == "sidecar.get" || event.Name == "sidecar.post" || event.Name == "sidecar.stop"
	if event.SidecarAttempt != 0 && !isSidecar {
		return errors.New("trace: sidecar attempt is not valid for event")
	}
	if event.Name == "sidecar.stop" && event.SidecarAttempt != 0 {
		return errors.New("trace: sidecar stop cannot have an attempt")
	}
	if (event.Name == "sidecar.get" || event.Name == "sidecar.post") && event.SidecarAttempt == 0 {
		return errors.New("trace: sidecar operation attempt is required")
	}
	return nil
}

func validateTimingFields(event TraceEvent, schema traceSchemaRules) error {
	durations := []time.Duration{event.ActorQueueWait, event.CallbackIPC, event.LocalDuration, event.ZoxideDuration,
		event.TransformDuration, event.LoadDuration}
	for _, duration := range durations {
		if duration < schema.DurationMin || duration > schema.DurationMax {
			return errors.New("trace: invalid duration")
		}
	}
	if (event.ActorQueueWait != 0 || event.TransformDuration != 0) &&
		event.Name != "generation.publish" && event.Name != "generation.discard" {
		return errors.New("trace: generation timing is not valid for event")
	}
	if event.LocalDuration != 0 && event.Name != "generation.publish" && event.Name != "generation.discard" &&
		event.Name != "sidecar.get" && event.Name != "sidecar.post" && event.Name != "sidecar.stop" {
		return errors.New("trace: local timing is not valid for event")
	}
	if event.ZoxideDuration != 0 && event.Name != "generation.publish" && event.Name != "generation.discard" && event.Name != "zoxide.enrichment" {
		return errors.New("trace: zoxide timing is not valid for event")
	}
	if event.CallbackIPC != 0 && event.Name != "callback.event" && event.Name != "callback.display" {
		return errors.New("trace: callback timing is not valid for event")
	}
	if event.LoadDuration != 0 && event.Name != "callback.load" {
		return errors.New("trace: load timing is not valid for event")
	}
	return nil
}

func validTraceOutcomeWithSchema(event, outcome string, schema traceSchemaRules) bool {
	return schemaContains(schema.EventOutcomes[event], outcome)
}

func validRendererWithSchema(renderer string, schema traceSchemaRules) bool {
	if schemaContains(schema.RendererBases, renderer) {
		return true
	}
	base, fallback := strings.CutSuffix(renderer, "-fallback")
	return fallback && base != "native" && schemaContains(schema.RendererBases, base)
}
