package integration

import (
	"errors"
	"fmt"
	"strings"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

type firstFrameMode string

const (
	firstFrameDisabled firstFrameMode = "disabled"
	firstFrameEnabled  firstFrameMode = "enabled"
)

type firstFrameMetric int64

type firstFrameTimestamp struct {
	wall      time.Time
	monotonic time.Time
}

type firstFrameMetrics struct {
	firstByteUS                                                           firstFrameMetric
	meaningfulFrameUS                                                     firstFrameMetric
	fzfStartUS                                                            firstFrameMetric
	zoxideTerminalUS                                                      firstFrameMetric
	previewDispatchUS                                                     firstFrameMetric
	previewCompleteUS                                                     firstFrameMetric
	firstBytePresent, meaningfulFramePresent, fzfStartPresent             bool
	zoxideTerminalPresent, previewDispatchPresent, previewCompletePresent bool
	sidecar                                                               *firstFrameSidecarMetrics
}

type firstFrameSidecarMetrics struct {
	getCount, postCount           int
	getDurationUS, postDurationUS int64
}

type firstFrameProcessCounts struct {
	total     int
	renderers map[string]int
}

type firstFrameCallbackCounts struct {
	info, display, preview, event, load int
}

type firstFrameProcessSnapshot struct {
	at      time.Time
	records []descendantProcessRecord
}

type firstFrameProcessBalance struct {
	starts, exits, live int
}

type firstFrameSample struct {
	mode                    firstFrameMode
	started                 time.Time
	firstConPTYByte         time.Time
	firstMeaningfulFrame    firstFrameTimestamp
	events                  []traceEvent
	processSnapshots        []firstFrameProcessSnapshot
	finalProcessRecords     []descendantProcessRecord
	exitedProcessIdentities []string
	processBalance          firstFrameProcessBalance
	processThroughFrame     firstFrameProcessCounts
	processPreviewComplete  firstFrameProcessCounts
	callbackThroughFrame    firstFrameCallbackCounts
	callbackPreviewComplete firstFrameCallbackCounts
	candidateCount          int
	metrics                 firstFrameMetrics
	sidecar                 *firstFrameSidecarMetrics
}

func firstFrameSampleFixture() firstFrameSample {
	started := time.Now().Add(-100 * time.Millisecond)
	meaningfulFrame := started.Add(5 * time.Millisecond)
	events := []traceEvent{
		firstFrameTraceEvent("session.start", 0, "cd"),
		firstFrameTraceEvent("generation.publish", time.Millisecond, "ok"),
		firstFrameTraceEvent("fzf.start", 2*time.Millisecond, "ok"),
		firstFrameTraceEvent("sidecar.get", 3*time.Millisecond, "success"),
		firstFrameTraceEvent("sidecar.post", 4*time.Millisecond, "success"),
		firstFrameTraceEvent("callback.info.start", 3*time.Millisecond, "started"),
		firstFrameTraceEvent("callback.info", 3*time.Millisecond, "ok"),
		firstFrameTraceEvent("callback.display.start", 4*time.Millisecond, "started"),
		firstFrameTraceEvent("callback.display", 4*time.Millisecond, "ok"),
		firstFrameTraceEvent("callback.event.start", 5*time.Millisecond, "started"),
		firstFrameTraceEvent("callback.event", 5*time.Millisecond, "up"),
		firstFrameTraceEvent("callback.preview.start", 6*time.Millisecond, "started"),
		firstFrameTraceEvent("callback.preview", 6*time.Millisecond, "ok"),
		firstFrameTraceEvent("preview.dispatch", 6*time.Millisecond, "ok"),
		firstFrameTraceEvent("preview.finished", 7*time.Millisecond, "ok"),
		firstFrameTraceEvent("callback.load.start", 8*time.Millisecond, "started"),
		firstFrameTraceEvent("callback.load", 8*time.Millisecond, "ok"),
		firstFrameTraceEvent("zoxide.enrichment", 9*time.Millisecond, "published"),
		firstFrameTraceEvent("fzf.exit", 10*time.Millisecond, "ok"),
		firstFrameTraceEvent("session.close", 11*time.Millisecond, "accepted"),
	}
	records := []descendantProcessRecord{
		{PID: 11, Identity: "11:fzf", CommandLine: "fzf --with-shell=shell-picker --fzf-shell"},
		{PID: 12, Identity: "12:preview", CommandLine: "shell-picker --fzf-shell p"},
		{PID: 13, Identity: "13:renderer", CommandLine: "eza --long"},
	}
	return firstFrameSample{
		mode: firstFrameEnabled, started: started,
		firstConPTYByte: started.Add(3 * time.Millisecond), firstMeaningfulFrame: firstFrameTimestamp{wall: meaningfulFrame.UTC(), monotonic: meaningfulFrame},
		events:              events,
		processSnapshots:    []firstFrameProcessSnapshot{{at: started.Add(5 * time.Millisecond), records: records}},
		finalProcessRecords: records, exitedProcessIdentities: []string{"11:fzf", "12:preview", "13:renderer"},
		processBalance:         firstFrameProcessBalance{starts: len(records), exits: len(records)},
		processPreviewComplete: firstFrameProcessCounts{total: len(records), renderers: map[string]int{"eza": 1}}, candidateCount: 7,
		metrics: firstFrameMetrics{firstByteUS: 3_000, meaningfulFrameUS: 5_000},
		sidecar: &firstFrameSidecarMetrics{getCount: 1, postCount: 1, getDurationUS: 100, postDurationUS: 200},
	}
}

func firstFrameTraceEvent(name string, offset time.Duration, outcome string) traceEvent {
	const session = "sha256:0123456789abcdef"
	base := time.Now().UTC().Add(-100 * time.Millisecond)
	event := traceEvent{Schema: integrationpkg.TraceSchema, Time: base.Add(offset).Format(time.RFC3339Nano),
		Session: session, Event: name, Outcome: outcome}
	switch name {
	case "generation.publish":
		event.Generation, event.CandidateCount, event.ZoxidePolicy, event.ZoxideOutcome = 1, 3, "cached", "pending"
	case "zoxide.enrichment":
		event.Generation, event.CandidateCount = 2, 7
		event.Outcome, event.ZoxidePolicy, event.ZoxideOutcome = "published", "cached", "ok"
		event.ZoxideAttempts, event.ZoxideStarts, event.ZoxideExits, event.ZoxideProcesses, event.ZoxideMaxLive = 1, 1, 1, 1, 1
	case "callback.load", "callback.load.start":
		event.Generation = 2
	case "sidecar.get":
		event.SidecarAttempt, event.LocalUS = 1, 100
	case "sidecar.post":
		event.SidecarAttempt, event.LocalUS = 1, 200
	case "preview.dispatch", "preview.finished":
		event.Renderer = "eza"
		if name == "preview.finished" {
			event.ChildStarts, event.MaxLiveChildren = 1, 1
		}
	}
	return event
}

func cloneFirstFrameSample(sample firstFrameSample) firstFrameSample {
	clone := sample
	clone.events = append([]traceEvent(nil), sample.events...)
	clone.finalProcessRecords = append([]descendantProcessRecord(nil), sample.finalProcessRecords...)
	clone.exitedProcessIdentities = append([]string(nil), sample.exitedProcessIdentities...)
	clone.processSnapshots = make([]firstFrameProcessSnapshot, len(sample.processSnapshots))
	for index, snapshot := range sample.processSnapshots {
		clone.processSnapshots[index] = snapshot
		clone.processSnapshots[index].records = append([]descendantProcessRecord(nil), snapshot.records...)
	}
	if sample.sidecar != nil {
		value := *sample.sidecar
		clone.sidecar = &value
	}
	return clone
}

func removeFirstFrameEvent(events []traceEvent, name string) []traceEvent {
	result := make([]traceEvent, 0, len(events))
	removed := false
	for _, event := range events {
		if event.Event == name && !removed {
			removed = true
			continue
		}
		result = append(result, event)
	}
	return result
}

func validateFirstFrameSample(sample firstFrameSample) error {
	if sample.mode != firstFrameDisabled && sample.mode != firstFrameEnabled {
		return errors.New("first-frame sample has no explicit enabled/disabled mode")
	}
	if sample.started.IsZero() || sample.firstConPTYByte.IsZero() || sample.firstMeaningfulFrame.wall.IsZero() || sample.firstMeaningfulFrame.monotonic.IsZero() {
		return errors.New("first-frame sample is missing an external marker")
	}
	if sample.firstConPTYByte.Before(sample.started) || sample.firstMeaningfulFrame.wall.Before(sample.started) || sample.firstMeaningfulFrame.monotonic.Before(sample.started) {
		return errors.New("first-frame external marker precedes sample start")
	}
	if len(sample.events) == 0 {
		return errors.New("first-frame trace is empty")
	}
	if _, err := countFirstFrameCallbackInvocations(sample.events, true); err != nil {
		return err
	}
	session := sample.events[0].Session
	if session == "" {
		return errors.New("first-frame trace has no session")
	}
	var previous time.Time
	counts := map[string]int{}
	var first, fzfStart, fzfExit, close time.Time
	for _, event := range sample.events {
		if err := integrationpkg.ValidateTraceRecordAt(event, time.Time{}); err != nil {
			return fmt.Errorf("first-frame trace validation: %w", err)
		}
		if event.Session != session {
			return errors.New("first-frame trace contains multiple sessions")
		}
		stamp, err := time.Parse(time.RFC3339Nano, event.Time)
		if err != nil {
			return fmt.Errorf("first-frame trace timestamp: %w", err)
		}
		if !previous.IsZero() && stamp.Before(previous) {
			return errors.New("first-frame trace timestamps decrease")
		}
		if !close.IsZero() && event.Event != "session.close" {
			return errors.New("first-frame trace contains an event after session.close")
		}
		previous = stamp
		counts[event.Event]++
		switch event.Event {
		case "session.start":
			first = stamp
		case "fzf.start":
			fzfStart = stamp
		case "fzf.exit":
			fzfExit = stamp
		case "session.close":
			close = stamp
		case "trace.error":
			return errors.New("first-frame trace contains an error marker")
		}
	}
	for _, name := range []string{"session.start", "fzf.start", "fzf.exit", "session.close"} {
		if counts[name] != 1 {
			return fmt.Errorf("first-frame trace marker %s count=%d", name, counts[name])
		}
	}
	if fzfStart.Before(first) || fzfExit.Before(fzfStart) || close.Before(fzfExit) {
		return errors.New("first-frame trace lifecycle timestamps reverse")
	}
	if sample.firstMeaningfulFrame.wall.Before(fzfStart) {
		return errors.New("first meaningful frame precedes fzf start")
	}
	if err := validateFirstFrameZoxide(sample.events); err != nil {
		return err
	}
	if err := validateFirstFrameProcessMarkers(sample); err != nil {
		return err
	}
	if err := validateFirstFrameSidecar(sample); err != nil {
		return err
	}
	return nil
}

func validateFirstFrameCandidateCount(expected int, sample firstFrameSample) error {
	if sample.candidateCount <= 0 {
		return errors.New("first-frame candidate count is zero")
	}
	if expected > 0 && sample.candidateCount != expected {
		return fmt.Errorf("first-frame candidate count=%d, want %d", sample.candidateCount, expected)
	}
	return nil
}

func validateFirstFrameZoxide(events []traceEvent) error {
	terminals := 0
	initialGeneration := uint64(0)
	for _, event := range events {
		if event.Event == "generation.publish" && event.ZoxideOutcome == "pending" && initialGeneration == 0 {
			initialGeneration = event.Generation
		}
		if event.Event != "zoxide.enrichment" {
			continue
		}
		terminals++
		if initialGeneration == 0 || event.Generation != initialGeneration+1 || event.ZoxideOutcome == "pending" || event.ZoxideStarts != event.ZoxideExits ||
			event.ZoxideStarts != event.ZoxideProcesses || event.ZoxideLive != 0 || event.ZoxideMaxLive > event.ZoxideStarts {
			return errors.New("first-frame zoxide process markers are unbalanced")
		}
	}
	if terminals != 1 {
		return fmt.Errorf("first-frame zoxide terminal count=%d", terminals)
	}
	return nil
}

func validateFirstFrameProcessMarkers(sample firstFrameSample) error {
	if len(sample.processSnapshots) == 0 || len(sample.finalProcessRecords) == 0 {
		return errors.New("first-frame process recorder has no complete session")
	}
	if sample.processBalance.starts <= 0 || sample.processBalance.starts != sample.processBalance.exits || sample.processBalance.live != 0 {
		return errors.New("first-frame process starts/exits are unbalanced")
	}
	previous := time.Time{}
	seen := make(map[string]bool)
	for _, snapshot := range sample.processSnapshots {
		if snapshot.at.Before(previous) {
			return errors.New("first-frame process marker timestamps decrease")
		}
		previous = snapshot.at
		for _, record := range snapshot.records {
			if record.PID <= 0 || record.Identity == "" || record.CommandLine == "" {
				return errors.New("first-frame process record is incomplete")
			}
			if strings.Contains(record.CommandLine, "--fzf-shell") && !strings.Contains(record.CommandLine, "--with-shell=") {
				category, err := firstFrameCallbackCategory(record.CommandLine)
				if err != nil || category == "" {
					return fmt.Errorf("first-frame callback identity is invalid: %w", err)
				}
			}
			seen[record.Identity] = true
		}
	}
	for _, record := range sample.finalProcessRecords {
		if !seen[record.Identity] {
			return errors.New("first-frame final process was not recorded during the session")
		}
	}
	finalIdentities := make(map[string]bool, len(sample.finalProcessRecords))
	for _, record := range sample.finalProcessRecords {
		if finalIdentities[record.Identity] {
			return errors.New("first-frame final process identities are duplicated")
		}
		finalIdentities[record.Identity] = true
	}
	verifiedExits := make(map[string]bool, len(sample.exitedProcessIdentities))
	for _, identity := range sample.exitedProcessIdentities {
		if identity == "" || verifiedExits[identity] || !finalIdentities[identity] {
			return errors.New("first-frame verified process exits are invalid")
		}
		verifiedExits[identity] = true
	}
	if len(verifiedExits) != len(finalIdentities) {
		return errors.New("first-frame verified process exits are incomplete")
	}
	if sample.processBalance.starts != len(finalIdentities) {
		return errors.New("first-frame process start count does not match observed identities")
	}
	if sample.processBalance.exits != len(verifiedExits) {
		return errors.New("first-frame process exit count does not match verified process exits")
	}
	return nil
}

func validateFirstFrameSidecar(sample firstFrameSample) error {
	get, post := 0, 0
	for _, event := range sample.events {
		if event.Event != "sidecar.get" && event.Event != "sidecar.post" {
			continue
		}
		if event.SidecarAttempt == 0 || event.LocalUS < 0 {
			return errors.New("first-frame sidecar marker is incomplete")
		}
		if event.Event == "sidecar.get" {
			get++
		} else {
			post++
		}
	}
	if sample.mode == firstFrameDisabled {
		if sample.sidecar != nil || get != 0 || post != 0 {
			return errors.New("disabled first-frame sample has sidecar metrics")
		}
		return nil
	}
	if sample.sidecar == nil || sample.sidecar.getCount != get || sample.sidecar.postCount != post {
		return errors.New("enabled first-frame sample sidecar counts are incomplete")
	}
	if get == 0 || post == 0 {
		return errors.New("enabled first-frame sample lacks a GET/POST observation")
	}
	return nil
}

func measureFirstFrameTrace(events []traceEvent, started, firstByte time.Time, meaningful firstFrameTimestamp) (firstFrameMetrics, error) {
	if started.IsZero() || firstByte.IsZero() || meaningful.wall.IsZero() || meaningful.monotonic.IsZero() {
		return firstFrameMetrics{}, errors.New("first-frame trace measurement has missing external marker")
	}
	meaningfulUS, err := firstFrameMonotonicDurationUS(started, meaningful.monotonic)
	if err != nil {
		return firstFrameMetrics{}, err
	}
	firstByteUS, err := firstFrameMonotonicDurationUS(started, firstByte)
	if err != nil {
		return firstFrameMetrics{}, err
	}
	metrics := firstFrameMetrics{firstByteUS: firstByteUS, meaningfulFrameUS: meaningfulUS,
		firstBytePresent: true, meaningfulFramePresent: true}
	if firstByte.Before(started) {
		return firstFrameMetrics{}, errors.New("first-frame trace measurement has negative external duration")
	}
	var getCount, postCount int
	var getDuration, postDuration int64
	var foundFZF, foundZoxide, foundDispatch, foundComplete bool
	startedWall := started.UTC()
	for _, event := range events {
		stamp, err := time.Parse(time.RFC3339Nano, event.Time)
		if err != nil {
			return firstFrameMetrics{}, err
		}
		elapsed := firstFrameDurationUS(startedWall, stamp)
		if stamp.Before(startedWall) {
			return firstFrameMetrics{}, errors.New("first-frame trace event precedes sample start")
		}
		switch event.Event {
		case "fzf.start":
			if !foundFZF {
				metrics.fzfStartUS, metrics.fzfStartPresent, foundFZF = elapsed, true, true
			}
		case "zoxide.enrichment":
			if !foundZoxide {
				metrics.zoxideTerminalUS, metrics.zoxideTerminalPresent, foundZoxide = elapsed, true, true
			}
		case "preview.dispatch":
			if !foundDispatch {
				metrics.previewDispatchUS, metrics.previewDispatchPresent, foundDispatch = elapsed, true, true
			}
		case "preview.finished":
			if !foundComplete {
				metrics.previewCompleteUS, metrics.previewCompletePresent, foundComplete = elapsed, true, true
			}
		case "sidecar.get":
			getCount++
			getDuration += event.LocalUS
		case "sidecar.post":
			postCount++
			postDuration += event.LocalUS
		}
	}
	if !foundFZF || !foundZoxide || !foundDispatch || !foundComplete {
		return firstFrameMetrics{}, errors.New("first-frame trace lacks a required timing marker")
	}
	if getCount != 0 || postCount != 0 {
		metrics.sidecar = &firstFrameSidecarMetrics{getCount: getCount, postCount: postCount,
			getDurationUS: getDuration, postDurationUS: postDuration}
	}
	return metrics, nil
}

func firstFrameDurationUS(start, end time.Time) firstFrameMetric {
	return firstFrameMetric(end.Sub(start).Microseconds())
}

func firstFrameMonotonicDurationUS(start, end time.Time) (firstFrameMetric, error) {
	if start.IsZero() || end.IsZero() {
		return 0, errors.New("first-frame monotonic timestamp is missing")
	}
	elapsed := end.Sub(start)
	if elapsed < 0 {
		return 0, errors.New("first-frame monotonic duration is negative")
	}
	return firstFrameMetric(elapsed.Microseconds()), nil
}

func countFirstFrameProcesses(records []descendantProcessRecord) (firstFrameProcessCounts, error) {
	counts := firstFrameProcessCounts{renderers: make(map[string]int)}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if record.Identity == "" || record.CommandLine == "" {
			return firstFrameProcessCounts{}, errors.New("first-frame process identity is incomplete")
		}
		if seen[record.Identity] {
			continue
		}
		seen[record.Identity] = true
		counts.total++
		if strings.Contains(record.CommandLine, "--fzf-shell") && !strings.Contains(record.CommandLine, "--with-shell=") {
			if _, err := firstFrameCallbackCategory(record.CommandLine); err != nil {
				return firstFrameProcessCounts{}, err
			}
			continue
		}
		if renderer := firstFrameRendererCategory(record.CommandLine); renderer != "" {
			counts.renderers[renderer]++
		}
	}
	return counts, nil
}
