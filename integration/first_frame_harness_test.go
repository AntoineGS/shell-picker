package integration

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	firstFrameSamples           = flag.Int("first-frame-samples", 30, "qualified warm first-frame sample pairs")
	firstFrameOutput            = flag.String("first-frame-output", "", "absolute first-frame comparison JSON output")
	firstFrameRawDir            = flag.String("first-frame-raw-dir", "", "absolute first-frame raw artifact directory")
	firstFrameBuildMetadataPath = flag.String("first-frame-build-metadata", "", "verified reproducible first-frame build metadata JSON")
	firstFrameSeed              = flag.Int64("first-frame-seed", 20260804, "first-frame interleave/bootstrap seed")
)

const (
	firstFrameColumns = 120
	firstFrameLines   = 35
)

type firstFrameTools struct {
	fzf, zoxide, eza                      string
	fzfVersion, zoxideVersion, ezaVersion string
}

type firstFramePublicSample struct {
	Pair                    int                    `json:"pair"`
	Mode                    firstFrameMode         `json:"mode"`
	SidecarEnabled          bool                   `json:"sidecar_enabled"`
	CandidateCount          int                    `json:"candidate_count"`
	Metrics                 firstFrameMetricsJSON  `json:"metrics"`
	CallbackThroughFrame    firstFrameCallbackJSON `json:"callback_invocations_through_meaningful_frame"`
	CallbackPreviewComplete firstFrameCallbackJSON `json:"callback_invocations_through_preview_complete"`
	ProcessThroughFrame     firstFrameProcessJSON  `json:"processes_through_meaningful_frame"`
	ProcessPreviewComplete  firstFrameProcessJSON  `json:"processes_through_preview_complete"`
	Sidecar                 *firstFrameSidecarJSON `json:"sidecar,omitempty"`
	RawArtifact             string                 `json:"raw_artifact"`
}

type firstFrameMetricsJSON struct {
	FirstConPTYByteUS int64 `json:"first_conpty_byte_us"`
	MeaningfulFrameUS int64 `json:"meaningful_current_frame_us"`
	FZFStartUS        int64 `json:"fzf_start_us"`
	ZoxideTerminalUS  int64 `json:"zoxide_terminal_us"`
	PreviewDispatchUS int64 `json:"preview_dispatch_us"`
	PreviewCompleteUS int64 `json:"preview_complete_us"`
}

type firstFrameProcessJSON struct {
	Total     int            `json:"total_descendant_starts"`
	Renderers map[string]int `json:"renderers"`
}

type firstFrameCallbackJSON struct {
	Total   int `json:"total_invocations"`
	Info    int `json:"info"`
	Display int `json:"display"`
	Preview int `json:"preview"`
	Event   int `json:"event"`
	Load    int `json:"load"`
}

type firstFrameRawArtifact struct {
	Schema                  int                    `json:"schema"`
	Pair                    int                    `json:"pair"`
	Mode                    firstFrameMode         `json:"mode"`
	SidecarEnabled          bool                   `json:"sidecar_enabled"`
	Events                  []traceEvent           `json:"events"`
	Metrics                 firstFrameMetricsJSON  `json:"metrics"`
	CallbackThroughFrame    firstFrameCallbackJSON `json:"callback_invocations_through_meaningful_frame"`
	CallbackPreviewComplete firstFrameCallbackJSON `json:"callback_invocations_through_preview_complete"`
	ProcessThroughFrame     firstFrameProcessJSON  `json:"process_through_frame"`
	ProcessPreviewComplete  firstFrameProcessJSON  `json:"processes_through_preview_complete"`
	Sidecar                 *firstFrameSidecarJSON `json:"sidecar,omitempty"`
}

type firstFrameRunReport struct {
	Schema             int                              `json:"schema"`
	Status             string                           `json:"status"`
	BaselineStatus     string                           `json:"baseline_status"`
	ToolQualification  string                           `json:"tool_qualification"`
	Seed               int64                            `json:"seed"`
	Samples            int                              `json:"samples"`
	WarmupPerMode      int                              `json:"warmup_per_mode"`
	TerminalColumns    int                              `json:"terminal_columns"`
	TerminalLines      int                              `json:"terminal_lines"`
	FixtureCandidates  *int                             `json:"fixture_candidates,omitempty"`
	BinaryFingerprint  string                           `json:"binary_sha256"`
	BuildQualification firstFrameBuildMetadata          `json:"build_qualification"`
	Metadata           integrationpkg.BenchmarkMetadata `json:"metadata"`
	Tools              map[string]string                `json:"tools"`
	Metrics            map[string]firstFrameJSONMetric  `json:"metrics"`
	SamplesByPair      []firstFramePublicSample         `json:"samples_by_pair"`
}

type firstFrameJSONMetric struct {
	Unit     string                      `json:"unit"`
	Enabled  *firstFrameJSONDistribution `json:"enabled,omitempty"`
	Disabled *firstFrameJSONDistribution `json:"disabled,omitempty"`
	Delta    *firstFrameJSONDelta        `json:"delta,omitempty"`
}

type firstFrameJSONDistribution struct {
	P50   int64              `json:"p50"`
	P95   int64              `json:"p95"`
	P50CI firstFrameInterval `json:"p50_bootstrap_95ci"`
	P95CI firstFrameInterval `json:"p95_bootstrap_95ci"`
}

type firstFrameJSONDelta struct {
	P50   int64              `json:"p50"`
	P95   int64              `json:"p95"`
	P50CI firstFrameInterval `json:"p50_bootstrap_95ci"`
	P95CI firstFrameInterval `json:"p95_bootstrap_95ci"`
}

func TestDedicatedFirstFrameTargets(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("first-frame ConPTY measurement requires Windows")
	}
	if os.Getenv("SHELL_PICKER_DEDICATED_PERF") != "1" {
		t.Skip("set SHELL_PICKER_DEDICATED_PERF=1 for qualified first-frame measurement")
	}
	if err := validateFirstFrameOptions(*performanceBinary, *firstFrameSamples, *firstFrameOutput, *firstFrameRawDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(*firstFrameRawDir, 0o700); err != nil {
		t.Fatalf("create first-frame raw directory: %v", err)
	}
	binary, err := filepath.Abs(*performanceBinary)
	if err != nil {
		t.Fatal(err)
	}
	buildQualification := requireFirstFrameBuildQualification(t, binary, *firstFrameBuildMetadataPath)
	metadata := collectBenchmarkMetadata(t, binary)
	binaryFingerprint := firstFrameBinaryFingerprint(t, binary)
	baseline := readHostBaseline(t, *performanceBaseline)
	baselineStatus := integrationpkg.QualifyBaseline(metadata, baseline)
	tools := requireFirstFrameTools(t)
	report := firstFrameRunReport{Schema: 1, Status: "blocked", BaselineStatus: baselineStatus,
		ToolQualification: "qualified", Seed: *firstFrameSeed, Samples: *firstFrameSamples, WarmupPerMode: 1,
		TerminalColumns: firstFrameColumns, TerminalLines: firstFrameLines,
		BinaryFingerprint: binaryFingerprint, BuildQualification: buildQualification, Metadata: sanitizeFirstFrameMetadata(metadata),
		Tools:   map[string]string{"fzf": tools.fzfVersion, "zoxide": tools.zoxideVersion, "eza": tools.ezaVersion},
		Metrics: make(map[string]firstFrameJSONMetric)}
	diagnostic := firstFrameDiagnosticMode(os.Getenv)
	shouldMeasure, blockedStatus := firstFrameMeasurementDecision(baselineStatus, diagnostic)
	if !shouldMeasure {
		writeFirstFrameReport(t, report)
		t.Logf("first-frame baseline blocker: %s", blockedStatus)
		return
	}
	expectedCandidates := warmFirstFrameModes(t, binary, tools)
	report.FixtureCandidates = &expectedCandidates
	pairs := make([]firstFramePair, *firstFrameSamples)
	public := make([]firstFramePublicSample, 0, *firstFrameSamples*2)
	for _, scheduled := range firstFrameSchedule(*firstFrameSamples, *firstFrameSeed) {
		sample, err := runFirstFrameSample(t, binary, tools, scheduled.mode, scheduled.pair, expectedCandidates)
		if err != nil {
			t.Fatalf("first-frame pair %d %s: %v", scheduled.pair, scheduled.mode, err)
		}
		if err := validateFirstFrameCandidateCount(expectedCandidates, sample); err != nil {
			t.Fatalf("pair %d %s: %v", scheduled.pair, scheduled.mode, err)
		}
		public = append(public, firstFramePublicSampleFor(scheduled.pair, sample))
		if scheduled.mode == firstFrameDisabled {
			pairs[scheduled.pair].disabled = sample
		} else {
			pairs[scheduled.pair].enabled = sample
		}
		writeFirstFrameRaw(t, scheduled.pair, sample)
	}
	comparison, err := summarizeAllFirstFramePairs(pairs, *firstFrameSeed)
	if err != nil {
		t.Fatal(err)
	}
	report.Status = "measured"
	report.Metrics = firstFrameJSONMetrics(comparison)
	report.SamplesByPair = public
	if diagnostic {
		report.Status = "diagnostic-unqualified"
		report.ToolQualification = "unqualified-diagnostic"
		writeFirstFrameReport(t, report)
		return
	}
	if err := firstFrameAcceptanceError(pairs, comparison); err != nil {
		report.Status = "fail"
		writeFirstFrameReport(t, report)
		t.Fatal(err)
	}
	report.Status = "pass"
	writeFirstFrameReport(t, report)
}

func validateFirstFrameOptions(binary string, samples int, output, rawDir string) error {
	if runtime.GOOS != "windows" {
		return errors.New("first-frame measurement requires Windows")
	}
	if err := validateDedicatedOptions(binary, samples); err != nil {
		return err
	}
	if samples < 30 {
		return errors.New("first-frame measurement requires at least 30 pairs")
	}
	for name, path := range map[string]string{"output": output, "raw directory": rawDir} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("first-frame %s must be an absolute approved-temp path", name)
		}
	}
	return nil
}

func requireFirstFrameTools(t *testing.T) firstFrameTools {
	t.Helper()
	fzfPath := requireRealFZF(t)
	if version := commandVersion(fzfPath, "--version"); !strings.HasPrefix(version, "0.74.1 ") {
		t.Fatalf("first-frame requires fzf 0.74.1, got %q", version)
	}
	zoxidePath, err := exec.LookPath("zoxide")
	if err != nil {
		t.Fatalf("real zoxide is unavailable: %v", err)
	}
	ezaPath, err := exec.LookPath("eza")
	if err != nil {
		t.Fatalf("real eza is unavailable: %v", err)
	}
	return firstFrameTools{fzf: fzfPath, zoxide: zoxidePath, eza: ezaPath,
		fzfVersion: commandVersion(fzfPath, "--version"), zoxideVersion: commandVersion(zoxidePath, "--version"),
		ezaVersion: commandVersion(ezaPath, "--version")}
}

func warmFirstFrameModes(t *testing.T, binary string, tools firstFrameTools) int {
	t.Helper()
	expected := 0
	for _, mode := range []firstFrameMode{firstFrameDisabled, firstFrameEnabled} {
		sample, err := runFirstFrameSample(t, binary, tools, mode, -1, 0)
		if err != nil {
			t.Fatalf("warm %s first-frame sample: %v", mode, err)
		}
		if err := validateFirstFrameCandidateCount(expected, sample); err != nil {
			t.Fatalf("warm %s: %v", mode, err)
		}
		if expected == 0 {
			expected = sample.candidateCount
		}
	}
	if expected <= 0 {
		t.Fatal("warm first-frame fixture produced no candidates")
	}
	return expected
}

func runFirstFrameSample(t *testing.T, binary string, tools firstFrameTools, mode firstFrameMode, pair, expectedCandidates int) (firstFrameSample, error) {
	t.Helper()
	fixture, environment, err := firstFrameFixture(t, binary, tools, mode)
	if err != nil {
		return firstFrameSample{}, err
	}
	args := []string{string(protocol.PickerCD), "--cwd", fixture.cwd, "--home", fixture.home, "--fzf", fixture.fzf,
		"--zoxide-policy", "cached", "--zoxide-timeout", "150ms"}
	started := time.Now()
	term := newTerminalSession(t, terminalConfig{Path: binary, Args: args, Environment: environment,
		Directory: fixture.cwd, Columns: firstFrameColumns, Lines: firstFrameLines})
	defer term.Close()
	frameAt := waitForFirstMeaningfulFrame(t, term, expectedCandidates)
	frameRecords := term.DescendantProcessRecords(t)
	term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
	term.WaitBarrier(testContext(t), barrier{Event: "preview.dispatch", Operation: "ok", Renderer: "eza", Count: 1})
	previewCounts, previewRecords, previewCompleteAt := waitForFirstFrameRendererEvidence(t, term)
	term.WaitBarrier(testContext(t), barrier{Event: "zoxide.enrichment", Operation: "published", Count: 1})
	if mode == firstFrameEnabled {
		term.WaitBarrier(testContext(t), barrier{Event: "sidecar.get", Operation: "success", Count: 1})
		term.WaitBarrier(testContext(t), barrier{Event: "sidecar.post", Operation: "success", Count: 1})
	}
	if err := term.Send(keyEsc); err != nil {
		return firstFrameSample{}, err
	}
	term.WaitBarrier(testContext(t), barrier{Event: "callback.event", Operation: "es", Count: 1})
	if err := term.Send([]byte("q")); err != nil {
		return firstFrameSample{}, err
	}
	if err := term.Wait(testContext(t)); err != nil {
		return firstFrameSample{}, err
	}
	term.WaitBarrier(testContext(t), barrier{Event: "session.close", Count: 1})
	finalRecords := term.DescendantProcessRecords(t)
	events := term.TraceEvents()
	callbackAtFrame, err := countFirstFrameCallbackInvocationsThrough(events, frameAt.wall)
	if err != nil {
		return firstFrameSample{}, err
	}
	callbackAtPreview, err := countFirstFrameCallbackInvocationsThrough(events, previewCompleteAt)
	if err != nil {
		return firstFrameSample{}, err
	}
	callbackAtFinal, err := countFirstFrameCallbackInvocations(events, true)
	if err != nil {
		return firstFrameSample{}, err
	}
	if mode == firstFrameDisabled && (callbackAtFinal.info == 0 || callbackAtFinal.display == 0) {
		return firstFrameSample{}, fmt.Errorf("disabled first-frame mode observed info=%d display=%d callback invocations", callbackAtFinal.info, callbackAtFinal.display)
	}
	if mode == firstFrameEnabled && (callbackAtFinal.info != 0 || callbackAtFinal.display != 0) {
		return firstFrameSample{}, fmt.Errorf("enabled first-frame mode observed info=%d display=%d callback invocations", callbackAtFinal.info, callbackAtFinal.display)
	}
	var exitedProcessIdentities []string
	if verifier, ok := term.(interface {
		VerifiedProcessExits(*testing.T, []descendantProcessRecord) []string
	}); ok {
		exitedProcessIdentities = verifier.VerifiedProcessExits(t, finalRecords)
	} else {
		return firstFrameSample{}, errors.New("terminal session lacks recorded-process exit verification")
	}
	outputTimer, ok := term.(interface{ FirstOutputAt() time.Time })
	if !ok {
		return firstFrameSample{}, errors.New("terminal session lacks first ConPTY byte timestamp")
	}
	firstByte := outputTimer.FirstOutputAt()
	metrics, err := measureFirstFrameTrace(events, started, firstByte, frameAt)
	if err != nil {
		return firstFrameSample{}, err
	}
	metrics.firstByteUS, err = firstFrameMonotonicDurationUS(started, firstByte)
	if err != nil {
		return firstFrameSample{}, err
	}
	metrics.firstBytePresent, metrics.meaningfulFramePresent = true, true
	processAtFrame, err := countFirstFrameProcesses(frameRecords)
	if err != nil {
		return firstFrameSample{}, err
	}
	candidateCount, err := firstFrameCandidateCountFromTrace(events)
	if err != nil {
		return firstFrameSample{}, err
	}
	sample := firstFrameSample{mode: mode, started: started, firstConPTYByte: firstByte, firstMeaningfulFrame: frameAt,
		events: events, processSnapshots: []firstFrameProcessSnapshot{{at: frameAt.wall, records: frameRecords}, {at: previewCompleteAt, records: previewRecords},
			{at: time.Now().UTC(), records: finalRecords}}, finalProcessRecords: finalRecords,
		exitedProcessIdentities: exitedProcessIdentities,
		processBalance:          firstFrameProcessBalance{starts: len(finalRecords), exits: len(exitedProcessIdentities)}, processThroughFrame: processAtFrame,
		processPreviewComplete: previewCounts, callbackThroughFrame: callbackAtFrame, callbackPreviewComplete: callbackAtPreview,
		metrics: metrics, sidecar: metrics.sidecar, candidateCount: candidateCount}
	if err := validateFirstFrameSample(sample); err != nil {
		return firstFrameSample{}, err
	}
	return sample, nil
}

func waitForFirstFrameRendererEvidence(t *testing.T, term terminalSession) (firstFrameProcessCounts, []descendantProcessRecord, time.Time) {
	t.Helper()
	ctx := testContext(t)
	for {
		events := term.TraceEvents()
		records := term.DescendantProcessRecords(t)
		if complete, ok := firstFrameLatestEzaPreviewComplete(events); ok {
			if counts, err := firstFrameRendererCountsFromEvidence(events, records); err == nil {
				stamp, err := time.Parse(time.RFC3339Nano, complete.Time)
				if err != nil {
					t.Fatalf("parse first-frame preview completion timestamp: %v", err)
				}
				return counts, records, stamp
			}
		}
		finished := firstFrameEzaPreviewCompleteCount(events)
		term.WaitBarrier(ctx, barrier{Event: "preview.finished", Operation: "ok", Renderer: "eza", Count: finished + 1})
	}
}

func firstFrameBinaryFingerprint(t *testing.T, binary string) string {
	t.Helper()
	fingerprint := firstFrameBinaryFingerprintForPath(binary)
	if fingerprint == "" {
		t.Fatalf("read first-frame binary %q", binary)
	}
	return fingerprint
}

func waitForFirstMeaningfulFrame(t *testing.T, term terminalSession, expectedCandidates int) firstFrameTimestamp {
	t.Helper()
	ctx := testContext(t)
	for {
		output := term.Output()
		if firstFrameScreenHasCandidateCount(output, expectedCandidates) {
			return captureFirstFrameTimestamp()
		}
		term.WaitOutputAfter(ctx, len(output))
	}
}

func firstFrameLabelTotal(label string) int {
	_, total, ok := strings.Cut(label, "/")
	if !ok {
		return 0
	}
	fields := strings.Fields(total)
	if len(fields) == 0 {
		return 0
	}
	total = fields[0]
	for index := range total {
		if total[index] < '0' || total[index] > '9' {
			return 0
		}
	}
	value := 0
	for index := range total {
		value = value*10 + int(total[index]-'0')
	}
	return value
}

func firstFrameScreenHasCandidateCount(output []byte, expected int) bool {
	if label, ok := currentListBorderLabel(output); ok {
		total := firstFrameLabelTotal(label)
		if total > 0 && (expected == 0 || total == expected) {
			return true
		}
	}
	screen := replayTerminalScreen(output)
	for row := 1; row <= screen.maxRow; row++ {
		for _, field := range strings.Fields(screen.line(row)) {
			total := firstFrameLabelTotal(field)
			if total > 0 && (expected == 0 || total == expected) {
				return true
			}
		}
	}
	return false
}

func firstFrameCandidateCountFromTrace(events []traceEvent) (int, error) {
	found := 0
	candidates := 0
	for _, event := range events {
		if event.Event == "zoxide.enrichment" && event.Outcome == "published" {
			found++
			if event.CandidateCount <= 0 {
				return 0, errors.New("first-frame authoritative candidate count is zero")
			}
			if candidates != 0 && candidates != event.CandidateCount {
				return 0, fmt.Errorf("first-frame authoritative candidate count changed from %d to %d", candidates, event.CandidateCount)
			}
			candidates = event.CandidateCount
		}
	}
	if found != 1 {
		return 0, fmt.Errorf("first-frame authoritative candidate event count=%d", found)
	}
	return candidates, nil
}
