package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

func TestFirstFrameValidationRejectsMissingReversedMixedAndMalformedSamples(t *testing.T) {
	base := firstFrameSampleFixture()
	cases := []struct {
		name   string
		mutate func(*firstFrameSample)
	}{
		{name: "missing session start", mutate: func(sample *firstFrameSample) {
			sample.events = removeFirstFrameEvent(sample.events, "session.start")
		}},
		{name: "missing fzf start", mutate: func(sample *firstFrameSample) {
			sample.events = removeFirstFrameEvent(sample.events, "fzf.start")
		}},
		{name: "reversed trace timestamp", mutate: func(sample *firstFrameSample) {
			sample.events[3].Time = sample.events[1].Time
		}},
		{name: "mixed sessions", mutate: func(sample *firstFrameSample) {
			sample.events[2].Session = "sha256:fedcba9876543210"
		}},
		{name: "malformed trace record", mutate: func(sample *firstFrameSample) {
			sample.events[2].Schema = 1
		}},
		{name: "trace error marker", mutate: func(sample *firstFrameSample) {
			sample.events = append(sample.events, firstFrameTraceEvent("trace.error", 12*time.Millisecond, "error"))
		}},
		{name: "duplicate session start", mutate: func(sample *firstFrameSample) {
			sample.events = append(sample.events, sample.events[0])
		}},
		{name: "event after close", mutate: func(sample *firstFrameSample) {
			sample.events = append(sample.events, firstFrameTraceEvent("sidecar.stop", 11*time.Millisecond, "requested"))
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sample := cloneFirstFrameSample(base)
			testCase.mutate(&sample)
			if err := validateFirstFrameSample(sample); err == nil {
				t.Fatal("malformed first-frame sample accepted")
			}
		})
	}
}

func TestFirstFrameValidationRequiresBalancedProcessesAndKnownCallbackCategories(t *testing.T) {
	base := firstFrameSampleFixture()
	cases := []struct {
		name   string
		mutate func(*firstFrameSample)
	}{
		{name: "unbalanced zoxide process", mutate: func(sample *firstFrameSample) {
			for index := range sample.events {
				if sample.events[index].Event == "zoxide.enrichment" {
					sample.events[index].ZoxideExits = 0
				}
			}
		}},
		{name: "unknown callback command", mutate: func(sample *firstFrameSample) {
			sample.processSnapshots[0].records = append(sample.processSnapshots[0].records,
				descendantProcessRecord{PID: 99, Identity: "99:unknown", CommandLine: "shell-picker --fzf-shell unknown"})
		}},
		{name: "missing callback identity", mutate: func(sample *firstFrameSample) {
			sample.processSnapshots[0].records[0].Identity = ""
		}},
		{name: "missing process exit balance", mutate: func(sample *firstFrameSample) {
			sample.finalProcessRecords = nil
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sample := cloneFirstFrameSample(base)
			testCase.mutate(&sample)
			if err := validateFirstFrameSample(sample); err == nil {
				t.Fatal("invalid process markers accepted")
			}
		})
	}
}

func TestFirstFrameValidationRejectsSyntheticProcessExitBalance(t *testing.T) {
	sample := firstFrameSampleFixture()
	sample.exitedProcessIdentities = nil
	if err := validateFirstFrameProcessMarkers(sample); err == nil || !strings.Contains(err.Error(), "verified process exits") {
		t.Fatalf("process balance without independently verified exits was accepted: %v", err)
	}
}

func TestFirstFrameValidationKeepsOptionalSidecarMetricsAbsentInDisabledMode(t *testing.T) {
	sample := firstFrameSampleFixture()
	sample.mode = firstFrameDisabled
	sample.sidecar = nil
	filtered := sample.events[:0]
	for _, event := range sample.events {
		if !strings.HasPrefix(event.Event, "sidecar.") {
			filtered = append(filtered, event)
		}
	}
	sample.events = filtered
	if err := validateFirstFrameSample(sample); err != nil {
		t.Fatal(err)
	}
	if sample.sidecar != nil {
		t.Fatal("disabled sample has an ambiguous zero-valued sidecar metric")
	}
}

func TestFirstFrameStatisticsReportPairedBootstrapDeltas(t *testing.T) {
	pairs := []firstFramePair{
		{disabled: firstFrameSample{metrics: firstFrameMetrics{meaningfulFrameUS: firstFrameMetric(100)}}, enabled: firstFrameSample{metrics: firstFrameMetrics{meaningfulFrameUS: firstFrameMetric(80)}}},
		{disabled: firstFrameSample{metrics: firstFrameMetrics{meaningfulFrameUS: firstFrameMetric(120)}}, enabled: firstFrameSample{metrics: firstFrameMetrics{meaningfulFrameUS: firstFrameMetric(90)}}},
		{disabled: firstFrameSample{metrics: firstFrameMetrics{meaningfulFrameUS: firstFrameMetric(140)}}, enabled: firstFrameSample{metrics: firstFrameMetrics{meaningfulFrameUS: firstFrameMetric(110)}}},
	}
	report, err := summarizeFirstFramePairs(pairs, 7)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := report.metrics["meaningful_frame"]
	if !ok || metric.enabled.P50US != 90 || metric.disabled.P50US != 120 {
		t.Fatalf("meaningful frame report=%+v", report)
	}
	if metric.delta == nil || metric.delta.P50US != -30 || metric.delta.LowerUS > metric.delta.UpperUS {
		t.Fatalf("meaningful frame delta=%+v", metric.delta)
	}
}

func TestFirstFrameTraceMetricsKeepOptionalSidecarFieldsExplicit(t *testing.T) {
	sample := firstFrameSampleFixture()
	metrics, err := measureFirstFrameTrace(sample.events, sample.started, sample.firstConPTYByte, sample.firstMeaningfulFrame)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.fzfStartUS <= 0 || metrics.zoxideTerminalUS <= 0 || metrics.previewDispatchUS <= 0 || metrics.previewCompleteUS <= 0 {
		t.Fatalf("required trace metrics=%+v", metrics)
	}
	if metrics.sidecar == nil || metrics.sidecar.getCount != 1 || metrics.sidecar.postCount != 1 ||
		metrics.sidecar.getDurationUS != 100 || metrics.sidecar.postDurationUS != 200 {
		t.Fatalf("sidecar metrics=%+v", metrics.sidecar)
	}
	filtered := sample.events[:0]
	for _, event := range sample.events {
		if !strings.HasPrefix(event.Event, "sidecar.") {
			filtered = append(filtered, event)
		}
	}
	sample.events = filtered
	metrics, err = measureFirstFrameTrace(sample.events, sample.started, sample.firstConPTYByte, sample.firstMeaningfulFrame)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.sidecar != nil {
		t.Fatalf("absent sidecar metrics=%+v, want nil", metrics.sidecar)
	}
}

func TestFirstFrameProcessCategoriesUseObservedIdentities(t *testing.T) {
	records := firstFrameSampleFixture().finalProcessRecords
	counts, err := countFirstFrameProcesses(records)
	if err != nil {
		t.Fatal(err)
	}
	if counts.total != 3 || counts.renderers["eza"] != 1 {
		t.Fatalf("process counts=%+v", counts)
	}
}

func TestFirstFrameRendererEvidenceUsesDelayedTraceAndProcessUnion(t *testing.T) {
	events := []traceEvent{firstFrameTraceEvent("preview.dispatch", time.Millisecond, "ok"), firstFrameTraceEvent("preview.finished", 20*time.Millisecond, "ok")}
	records := []descendantProcessRecord{{PID: 13, Identity: "13:renderer", CommandLine: "eza --long"}}
	counts, err := firstFrameRendererCountsFromEvidence(events, records)
	if err != nil {
		t.Fatal(err)
	}
	if counts.renderers["eza"] != 1 {
		t.Fatalf("renderer counts=%+v, want one authoritative eza renderer", counts)
	}
}

func TestFirstFrameRendererEvidenceRejectsMissingEza(t *testing.T) {
	events := []traceEvent{firstFrameTraceEvent("preview.dispatch", time.Millisecond, "ok"), firstFrameTraceEvent("preview.finished", 20*time.Millisecond, "ok")}
	events[len(events)-1].Renderer = "native"
	if _, err := firstFrameRendererCountsFromEvidence(events, nil); err == nil {
		t.Fatal("renderer evidence without eza was accepted")
	}
}

type delayedRendererTerminalStub struct {
	resultFinalTerminalStub
	waits int
}

func (term *delayedRendererTerminalStub) DescendantProcessRecords(*testing.T) []descendantProcessRecord {
	return nil
}

func (term *delayedRendererTerminalStub) WaitBarrier(_ context.Context, wanted barrier) traceEvent {
	term.waits++
	term.events = append(term.events, traceEvent{Event: wanted.Event, Renderer: wanted.Renderer, Outcome: wanted.Operation,
		ChildStarts: 1, Time: time.Now().UTC().Format(time.RFC3339Nano), Session: "sha256:0123456789abcdef", Schema: 2})
	return term.events[len(term.events)-1]
}

func TestFirstFrameRendererEvidenceWaitsForDelayedFinishedTrace(t *testing.T) {
	term := &delayedRendererTerminalStub{resultFinalTerminalStub: resultFinalTerminalStub{events: []traceEvent{
		{Event: "preview.dispatch", Renderer: "eza", Outcome: "ok", Time: time.Now().UTC().Format(time.RFC3339Nano), Session: "sha256:0123456789abcdef", Schema: 2},
	}}}
	counts, records, completeAt := waitForFirstFrameRendererEvidence(t, term)
	if term.waits != 1 || len(records) != 0 || counts.total != 1 || counts.renderers["eza"] != 1 || completeAt.IsZero() {
		t.Fatalf("waits=%d records=%+v counts=%+v complete_at=%s, want one causal wait and trace eza", term.waits, records, counts, completeAt)
	}
}

func TestFirstFrameCandidateCountRequiresOnePositiveAuthoritativeFixture(t *testing.T) {
	valid := []traceEvent{firstFrameTraceEvent("zoxide.enrichment", time.Millisecond, "published")}
	if got, err := firstFrameCandidateCountFromTrace(valid); err != nil || got != 7 {
		t.Fatalf("valid candidate evidence=%d err=%v, want 7", got, err)
	}
	for name, events := range map[string][]traceEvent{
		"missing": nil,
		"zero":    {{Event: "zoxide.enrichment", Outcome: "published", CandidateCount: 0}},
		"mismatch": {
			{Event: "zoxide.enrichment", Outcome: "published", CandidateCount: 7},
			{Event: "zoxide.enrichment", Outcome: "published", CandidateCount: 8},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := firstFrameCandidateCountFromTrace(events); err == nil {
				t.Fatal("invalid candidate evidence was accepted")
			}
		})
	}
}

func TestFirstFrameCandidateCountRejectsZeroAndMismatchAcrossSamples(t *testing.T) {
	valid := firstFrameSampleFixture()
	if err := validateFirstFrameCandidateCount(7, valid); err != nil {
		t.Fatal(err)
	}
	for name, sample := range map[string]firstFrameSample{
		"zero": func() firstFrameSample {
			value := cloneFirstFrameSample(valid)
			value.candidateCount = 0
			return value
		}(),
		"mismatch": func() firstFrameSample {
			value := cloneFirstFrameSample(valid)
			value.candidateCount = 8
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateFirstFrameCandidateCount(7, sample); err == nil {
				t.Fatal("invalid candidate count was accepted")
			}
		})
	}
}

func TestFirstFrameReportMetricsCarryExplicitUnits(t *testing.T) {
	report := firstFrameComparisonReport{metrics: map[string]firstFrameMetricReport{
		"meaningful_frame":       {enabledPresent: true, enabled: firstFrameDistribution{firstFramePercentiles: firstFramePercentiles{P50US: 100, P95US: 200}}},
		"callback_through_frame": {enabledPresent: true, enabled: firstFrameDistribution{firstFramePercentiles: firstFramePercentiles{P50US: 1, P95US: 2}}},
	}}
	data, err := json.Marshal(firstFrameJSONMetrics(report))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"unit":"us"`) || !strings.Contains(text, `"unit":"count"`) || !strings.Contains(text, `"p50":100`) || !strings.Contains(text, `"p50":1`) {
		t.Fatalf("explicit metric units missing: %s", text)
	}
	if strings.Contains(text, `callback_through_frame":{"unit":"count","enabled":{"p50_us"`) {
		t.Fatalf("count metric carries duration field: %s", text)
	}
	for name, metric := range firstFrameJSONMetrics(report) {
		if metric.Unit == "" {
			t.Fatalf("metric %q has no unit", name)
		}
	}
}

func TestFirstFrameReportPrivacyUsesRelativeArtifactsAndRedactedMetadata(t *testing.T) {
	metadata := integrationpkg.BenchmarkMetadata{Hostname: "host-secret", Filesystem: `C:\Users\alice\private`, PowerPlan: `C:\Users\alice\plan`, Terminal: "xterm"}
	redacted := sanitizeFirstFrameMetadata(metadata)
	if redacted.Hostname == metadata.Hostname || strings.Contains(redacted.Hostname, "host-secret") {
		t.Fatalf("hostname was not redacted: %+v", redacted)
	}
	if strings.Contains(redacted.Filesystem, "alice") || strings.Contains(redacted.PowerPlan, "alice") {
		t.Fatalf("user path remained in report metadata: %+v", redacted)
	}
	root := `C:\Users\alice\artifacts`
	artifact := filepath.Join(root, "pair-000-disabled.json")
	relative := firstFrameRelativeArtifactPath(root, artifact)
	if filepath.IsAbs(relative) || relative != "pair-000-disabled.json" {
		t.Fatalf("artifact path=%q, want relative pair path", relative)
	}
	data, err := json.Marshal(firstFramePublicSampleFor(0, firstFrameSampleFixture()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, `C:\Users\alice`) || strings.Contains(text, `"raw_artifact":"C:\`) || strings.Contains(text, "query-canary") || strings.Contains(text, "state-canary") {
		t.Fatalf("report privacy canary leaked: %s", text)
	}
}

func TestFirstFrameScheduleInterleavesEnabledAndDisabledPairs(t *testing.T) {
	schedule := firstFrameSchedule(8, 19)
	if len(schedule) != 16 {
		t.Fatalf("schedule length=%d", len(schedule))
	}
	for index := 0; index < len(schedule); index += 2 {
		if schedule[index].pair != schedule[index+1].pair || schedule[index].mode == schedule[index+1].mode {
			t.Fatalf("schedule pair %d=%+v/%+v is not an interleaved pair", index/2, schedule[index], schedule[index+1])
		}
	}
	first := firstFrameMode(firstFrameDisabled)
	if schedule[0].mode != first && schedule[0].mode != firstFrameEnabled {
		t.Fatalf("schedule has invalid first mode=%q", schedule[0].mode)
	}
}

func TestFirstFrameMakeTargetRequiresCallerSuppliedTempArtifacts(t *testing.T) {
	command := exec.Command("make", "-n", "performance-first-frame")
	command.Dir = ".."
	command.Env = append(os.Environ(),
		"SHELL_PICKER_DEDICATED_PERF=1", "SHELL_PICKER_FIRST_FRAME_BINARY=C:/temp/picker.exe",
		"SHELL_PICKER_FIRST_FRAME_TEST_BINARY=C:/temp/perf.test.exe", "SHELL_PICKER_FIRST_FRAME_BUILD_METADATA=C:/temp/build.json", "SHELL_PICKER_FIRST_FRAME_BASELINE=C:/temp/baseline.json",
		"SHELL_PICKER_FIRST_FRAME_OUTPUT=C:/temp/report.json", "SHELL_PICKER_FIRST_FRAME_RAW_DIR=C:/temp/raw",
		"SHELL_PICKER_FIRST_FRAME_SAMPLES=30")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make dry run: %v\n%s", err, output)
	}
	text := string(output)
	for _, required := range []string{"verify-first-frame-build.ps1", "TestDedicatedBaseline", "TestDedicatedFirstFrameTargets", "C:/temp/picker.exe", "C:/temp/perf.test.exe", "-first-frame-build-metadata", "C:/temp/build.json", "-first-frame-output", "-first-frame-raw-dir", "-first-frame-samples"} {
		if !strings.Contains(text, required) {
			t.Fatalf("first-frame make target lacks %q:\n%s", required, text)
		}
	}
	buildScript, err := os.ReadFile("../scripts/verify-first-frame-build.ps1")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"-buildvcs=false", "-trimpath", "-ldflags=-buildid=", "stable_builds = 3", "source_head", "source_fingerprint", "Get-SourceIdentity", "Assert-Present"} {
		if !strings.Contains(string(buildScript), required) {
			t.Fatalf("reproducible first-frame build script lacks %q", required)
		}
	}
	if strings.Contains(text, "first-frame-output first-frame") || strings.Contains(text, "first-frame-raw-dir raw") {
		t.Fatalf("first-frame target writes a relative artifact path:\n%s", text)
	}
	if strings.Contains(text, "mkdir -p bin") || strings.Contains(text, "bin/shell-picker") {
		t.Fatalf("first-frame target writes build artifacts into the repository:\n%s", text)
	}
}

func TestFirstFrameJSONOmitsDisabledOptionalSidecarMetrics(t *testing.T) {
	sample := firstFrameSampleFixture()
	sample.mode = firstFrameDisabled
	sample.sidecar = nil
	value := firstFramePublicSampleFor(3, sample)
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"sidecar"`) {
		t.Fatalf("disabled sample serialized an absent sidecar metric: %s", data)
	}
}

func TestFirstFrameCallbackCountsUseTraceInvocationsNotProcessSnapshots(t *testing.T) {
	base := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	session := "sha256:0123456789abcdef"
	events := []traceEvent{
		{Schema: 2, Time: base.Format(time.RFC3339Nano), Session: session, Event: "session.start", Outcome: "cd"},
		{Schema: 2, Time: base.Add(500 * time.Microsecond).Format(time.RFC3339Nano), Session: session, Event: "callback.info.start", Outcome: "started"},
		{Schema: 2, Time: base.Add(time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "callback.info", Outcome: "ok"},
		{Schema: 2, Time: base.Add(1500 * time.Microsecond).Format(time.RFC3339Nano), Session: session, Event: "callback.display.start", Outcome: "started"},
		{Schema: 2, Time: base.Add(2 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "callback.display", Outcome: "ok"},
		{Schema: 2, Time: base.Add(2500 * time.Microsecond).Format(time.RFC3339Nano), Session: session, Event: "callback.preview.start", Outcome: "started"},
		{Schema: 2, Time: base.Add(3 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "callback.preview", Outcome: "ok"},
		{Schema: 2, Time: base.Add(3500 * time.Microsecond).Format(time.RFC3339Nano), Session: session, Event: "callback.event.start", Outcome: "started"},
		{Schema: 2, Time: base.Add(4 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "callback.event", Outcome: "es"},
		{Schema: 2, Time: base.Add(4500 * time.Microsecond).Format(time.RFC3339Nano), Session: session, Event: "callback.load.start", Generation: 2, Outcome: "started"},
		{Schema: 2, Time: base.Add(5 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "callback.load", Generation: 2, Outcome: "ok"},
		{Schema: 2, Time: base.Add(6 * time.Millisecond).Format(time.RFC3339Nano), Session: session, Event: "session.close", Outcome: "aborted"},
	}
	counts, err := countFirstFrameCallbackInvocations(events, true)
	if err != nil {
		t.Fatal(err)
	}
	if counts.info != 1 || counts.display != 1 || counts.preview != 1 || counts.event != 1 || counts.load != 1 || counts.callbackTotal() != 5 {
		t.Fatalf("trace callback counts=%+v", counts)
	}
	counts, err = countFirstFrameCallbackInvocations(events[:1], false)
	if err != nil {
		t.Fatal(err)
	}
	if counts.callbackTotal() != 0 {
		t.Fatalf("snapshot-only callback counts=%+v", counts)
	}
}

func TestFirstFrameBuildQualificationRequiresStablePresentReproducibleOutputs(t *testing.T) {
	valid := firstFrameBuildMetadata{
		Schema: 1, BuildFlags: append([]string(nil), firstFrameReproducibleBuildFlags...),
		ProductionHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		HarnessHash:    "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		SourceHead:     "0123456789abcdef0123456789abcdef01234567",
		SourceHash:     "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		StableBuilds:   3, FilesPresent: true, DefenderState: "running",
	}
	if err := validateFirstFrameBuildMetadata(valid); err != nil {
		t.Fatalf("valid build metadata rejected: %v", err)
	}
	for _, mutate := range []func(*firstFrameBuildMetadata){
		func(metadata *firstFrameBuildMetadata) { metadata.BuildFlags = []string{"-trimpath"} },
		func(metadata *firstFrameBuildMetadata) { metadata.StableBuilds = 2 },
		func(metadata *firstFrameBuildMetadata) { metadata.FilesPresent = false },
		func(metadata *firstFrameBuildMetadata) { metadata.ProductionHash = "" },
		func(metadata *firstFrameBuildMetadata) { metadata.HarnessHash = "" },
		func(metadata *firstFrameBuildMetadata) { metadata.SourceHead = "stale" },
		func(metadata *firstFrameBuildMetadata) { metadata.SourceHash = "" },
	} {
		metadata := valid
		mutate(&metadata)
		if err := validateFirstFrameBuildMetadata(metadata); err == nil {
			t.Fatalf("invalid build metadata accepted: %+v", metadata)
		}
	}
}
