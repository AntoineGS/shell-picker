package integration

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

var (
	performanceBinary   = flag.String("binary", "", "prebuilt shell-picker binary")
	performanceSamples  = flag.Int("samples", 50, "warm sample count")
	performanceOutput   = flag.String("output", "performance.json", "JSON output path")
	performanceBaseline = flag.String("baseline", "host-baseline.json", "host baseline path")
	performanceTraceDir = flag.String("trace-dir", "", "optional dedicated per-sample trace directory")
)

var errPrebuiltBinaryRequired = errors.New("dedicated performance requires prebuilt -binary")

func TestDedicatedHarnessDefaultsAndValidation(t *testing.T) {
	if *performanceSamples != 50 {
		t.Fatalf("default samples=%d", *performanceSamples)
	}
	if err := validateDedicatedOptions("", 50); !errors.Is(err, errPrebuiltBinaryRequired) {
		t.Fatalf("missing binary error=%v", err)
	}
	if err := validateDedicatedOptions(os.Args[0], 0); err == nil {
		t.Fatal("zero samples accepted")
	}
	if err := validateDedicatedOptions(os.Args[0], 7); err != nil {
		t.Fatalf("explicit smoke samples rejected: %v", err)
	}
}

func TestDedicatedNavigationEndsAtActionWriteNotCallbackReap(t *testing.T) {
	receipt := time.Now().UTC().Truncate(time.Microsecond)
	marker := performanceMarker{Start: receipt.Add(-time.Millisecond).UnixNano(),
		ActionWritten: receipt.Add(5 * time.Millisecond).UnixNano(), Reaped: receipt.Add(50 * time.Millisecond).UnixNano()}
	path := filepath.Join(t.TempDir(), "marker.json")
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	duration, err := dedicatedDuration([]traceEvent{{Event: "callback.event", Time: receipt.Format(time.RFC3339Nano)}}, path, "event")
	if err != nil || duration != 5*time.Millisecond {
		t.Fatalf("duration=%v err=%v; reap delta=%v", duration, err, time.Unix(0, marker.Reaped).Sub(receipt))
	}
}

func validateDedicatedOptions(binary string, samples int) error {
	if binary == "" {
		return errPrebuiltBinaryRequired
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() {
		return errPrebuiltBinaryRequired
	}
	if samples <= 0 || samples > 10_000 {
		return errors.New("dedicated performance samples must be in 1..10000")
	}
	return nil
}

func TestDedicatedBaseline(t *testing.T) {
	requireDedicatedPerformance(t)
	metadata := collectBenchmarkMetadata(t, *performanceBinary)
	metrics := []integrationpkg.BaselineMetric{
		measureBaseline(t, "child-spawn", *performanceSamples, func() error {
			command := exec.Command(*performanceBinary, "version")
			command.Stdout, command.Stderr = io.Discard, io.Discard
			return command.Run()
		}),
		measureLoopbackBaseline(t, *performanceSamples),
		measureReadDirBaseline(t, *performanceSamples),
	}
	baseline := integrationpkg.HostBaseline{Schema: 1, Fingerprint: integrationpkg.MetadataFingerprint(metadata),
		Metadata: metadata, Metrics: metrics}
	writePerformanceJSON(t, *performanceOutput, baseline)
}

type dedicatedTargetOutput struct {
	Schema              int                              `json:"schema"`
	Status              string                           `json:"status"`
	Fingerprint         string                           `json:"fingerprint"`
	BaselineFingerprint string                           `json:"baseline_fingerprint,omitempty"`
	Metadata            integrationpkg.BenchmarkMetadata `json:"metadata"`
	Reports             []integrationpkg.BenchmarkReport `json:"reports"`
}

type dedicatedScenario struct {
	name, policy, zoxideMode, action string
	timeout                          time.Duration
	generation                       uint64
	expected                         integrationpkg.BenchmarkCounters
	expectedZoxideOutcome            string
}

func TestDedicatedTargets(t *testing.T) {
	requireDedicatedPerformance(t)
	binary, err := filepath.Abs(*performanceBinary)
	if err != nil {
		t.Fatal(err)
	}
	metadata := collectBenchmarkMetadata(t, binary)
	baseline := readHostBaseline(t, *performanceBaseline)
	status := integrationpkg.QualifyBaseline(metadata, baseline)
	zoxideSource := buildPerformanceZoxideHelper(t)
	zoxideDirectory := t.TempDir()
	zoxideName := "zoxide"
	if runtime.GOOS == "windows" {
		zoxideName += ".exe"
	}
	zoxideExecutable := filepath.Join(zoxideDirectory, zoxideName)
	if err := copyExecutable(zoxideSource, zoxideExecutable); err != nil {
		t.Fatal(err)
	}
	warmPerformanceZoxideHelper(t, zoxideExecutable)
	if *performanceTraceDir != "" {
		if err := os.MkdirAll(*performanceTraceDir, 0o700); err != nil {
			t.Fatalf("create dedicated trace directory: %v", err)
		}
	}
	defaultTimeout := 75 * time.Millisecond
	if runtime.GOOS == "windows" {
		defaultTimeout = 150 * time.Millisecond
	}
	scenarios := dedicatedPickerScenarios(defaultTimeout)
	reports := make([]integrationpkg.BenchmarkReport, 0, len(scenarios))
	for _, scenario := range scenarios {
		scenario := scenario
		sampleNumber := 0
		report, err := integrationpkg.RunBenchmark(context.Background(), integrationpkg.BenchmarkOptions{
			Scenario: scenario.name, Samples: *performanceSamples, Policy: scenario.policy, Timeout: scenario.timeout,
			Expected: &scenario.expected, Metadata: metadata,
			Measure: func(context.Context) (integrationpkg.BenchmarkSample, error) {
				sampleNumber++
				return runDedicatedPickerSample(t, binary, scenario, zoxideExecutable, sampleNumber)
			},
		})
		if err != nil {
			t.Fatalf("scenario %s: %v", scenario.name, err)
		}
		if scenario.zoxideMode == "blocked" {
			if report.StartupDuration == nil || report.EnrichmentDuration == nil ||
				report.StartupDuration.P50US >= report.EnrichmentDuration.P50US {
				t.Fatalf("blocked source did not separate startup/enrichment: %+v", report)
			}
			if report.LifecycleDuration == nil || report.LifecycleDuration.P50US < report.EnrichmentDuration.P50US {
				t.Fatalf("blocked source lifecycle=%+v enrichment=%+v", report.LifecycleDuration, report.EnrichmentDuration)
			}
		}
		reports = append(reports, report)
	}
	reports = append(reports, runDedicatedCandidateReports(t, *performanceSamples, metadata, defaultTimeout)...)
	output := dedicatedTargetOutput{Schema: 1, Status: status, Fingerprint: integrationpkg.MetadataFingerprint(metadata),
		BaselineFingerprint: baseline.Fingerprint, Metadata: metadata, Reports: reports}
	writePerformanceJSON(t, *performanceOutput, output)
	if status == "qualified" {
		enforceDedicatedGoals(t, reports)
	}
}

func dedicatedPickerScenarios(defaultTimeout time.Duration) []dedicatedScenario {
	return []dedicatedScenario{
		{name: "startup-local-only", policy: "cached", zoxideMode: "empty", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1), expectedZoxideOutcome: "ok"},
		{name: "startup-zoxide-present", policy: "cached", zoxideMode: "present", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1), expectedZoxideOutcome: "ok"},
		{name: "startup-zoxide-missing", policy: "cached", zoxideMode: "missing", timeout: defaultTimeout,
			expected: zoxideCounters(1, 0), expectedZoxideOutcome: "missing"},
		{name: "startup-zoxide-spawn-failure", policy: "cached", zoxideMode: "spawn-failure", timeout: defaultTimeout,
			expected: zoxideCounters(1, 0), expectedZoxideOutcome: "process-error"},
		{name: "startup-zoxide-blocked", policy: "cached", zoxideMode: "blocked", timeout: time.Second,
			expected: zoxideCounters(1, 1), expectedZoxideOutcome: "ok"},
		{name: "startup-zoxide-timeout", policy: "cached", zoxideMode: "timeout", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1), expectedZoxideOutcome: "timeout"},
		{name: "navigation-local-only", policy: "cached", zoxideMode: "present", action: "event", timeout: defaultTimeout, generation: 2,
			expected: integrationpkg.BenchmarkCounters{}, expectedZoxideOutcome: "not-run"},
		{name: "preview-dispatch", policy: "cached", zoxideMode: "present", action: "preview", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1), expectedZoxideOutcome: "ok"},
	}
}

func requireDedicatedPerformance(t *testing.T) {
	t.Helper()
	if os.Getenv("SHELL_PICKER_DEDICATED_PERF") != "1" {
		t.Skip("set SHELL_PICKER_DEDICATED_PERF=1 for metadata-qualified dedicated measurement")
	}
	if err := validateDedicatedOptions(*performanceBinary, *performanceSamples); err != nil {
		t.Fatal(err)
	}
}

func runDedicatedPickerSample(t *testing.T, binary string, scenario dedicatedScenario, zoxideExecutable string, sampleNumber int) (integrationpkg.BenchmarkSample, error) {
	t.Helper()
	root := t.TempDir()
	for index := 0; index < 32; index++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("entry-%02d", index)), 0o700); err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
	}
	marker := filepath.Join(root, "marker.json")
	var err error
	var gate net.Listener
	var releaseGate chan struct{}
	var gateDone chan struct{}
	var releaseGateOnce sync.Once
	if scenario.zoxideMode == "blocked" {
		gate, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
		releaseGate, gateDone = make(chan struct{}), make(chan struct{})
		go servePerformanceGate(gate, releaseGate, gateDone)
		defer func() {
			releaseGateOnce.Do(func() { close(releaseGate) })
			_ = gate.Close()
			<-gateDone
		}()
	}
	harness, err := filepath.Abs(os.Args[0])
	if err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	zoxideName := "zoxide"
	if runtime.GOOS == "windows" {
		zoxideName += ".exe"
	}
	zoxidePath := filepath.Join(tools, zoxideName)
	if scenario.zoxideMode == "spawn-failure" {
		fixture := newSpawnFailureExecutable(t)
		if err := os.Rename(fixture, zoxidePath); err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
	}
	environment := replaceEnvironment(os.Environ(), parityHelperEnvironment+"=performance", "GO_PERF_HELPER=fzf", "GO_PERF_ZOXIDE_MODE="+scenario.zoxideMode,
		"GO_PERF_ZOXIDE_PATH="+root, "GO_PERF_FZF_ACTION="+scenario.action, "GO_PERF_MARKER="+marker,
		"TERM=xterm-256color", "XDG_CACHE_HOME="+filepath.Join(root, "cache"), "LOCALAPPDATA="+filepath.Join(root, "cache"))
	if gate != nil {
		environment = replaceEnvironment(environment, "GO_PERF_ZOXIDE_GATE="+gate.Addr().String())
	}
	args := []string{"cd", "--cwd", root, "--home", root, "--fzf", harness, "--zoxide-policy", scenario.policy,
		"--zoxide-timeout", scenario.timeout.String()}
	pathEntries := []string{tools}
	if scenario.zoxideMode != "missing" {
		pathEntries = append(pathEntries, filepath.Dir(zoxideExecutable))
	}
	pathEntries = append(pathEntries, filepath.Dir(binary))
	environment = replaceEnvironment(environment, "PATH="+strings.Join(pathEntries, string(os.PathListSeparator)))
	term := newTerminalSession(t, terminalConfig{Path: binary, Args: args, Environment: environment,
		Directory: root, Columns: 120, Lines: 35})
	defer term.Close()
	if scenario.zoxideMode == "blocked" {
		term.WaitBarrier(testContext(t), barrier{Event: "fzf.start", Count: 1})
		releaseGateOnce.Do(func() { close(releaseGate) })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := term.Wait(ctx); err != nil {
		events := term.TraceEvents()
		if traceErr := archiveDedicatedTrace(scenario, sampleNumber, events); traceErr != nil {
			return integrationpkg.BenchmarkSample{}, errors.Join(fmt.Errorf("picker wait: %w", err), traceErr)
		}
		summary := make([]string, 0, len(events))
		for _, event := range events {
			summary = append(summary, event.Event+":"+event.Outcome)
		}
		return integrationpkg.BenchmarkSample{}, fmt.Errorf("picker wait: %w; trace=%v", err, summary)
	}
	events := term.TraceEvents()
	if err := archiveDedicatedTrace(scenario, sampleNumber, events); err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	measurement, err := parseDedicatedTraceSample(events, true)
	if err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	if err := assertDedicatedZoxideOutcome(events, scenario); err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	counters, err := traceBenchmarkCounters(events, scenario.generation)
	if err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	if scenario.expectedZoxideOutcome != "" {
		actual := traceBenchmarkZoxideOutcome(events, scenario.generation)
		if actual != scenario.expectedZoxideOutcome {
			return integrationpkg.BenchmarkSample{}, fmt.Errorf("zoxide outcome=%q want %q", actual, scenario.expectedZoxideOutcome)
		}
	}
	duration, err := dedicatedDuration(events, marker, scenario.action)
	if err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	startupDuration, lifecycleDuration := measurement.StartupDuration, measurement.LifecycleDuration
	sample := integrationpkg.BenchmarkSample{Duration: duration, StartupDuration: &startupDuration,
		EnrichmentDuration: measurement.EnrichmentDuration, LifecycleDuration: &lifecycleDuration,
		BenchmarkCounters: counters}
	if scenario.action != "" {
		sample.ActionDuration = &duration
	}
	return sample, nil
}

func archiveDedicatedTrace(scenario dedicatedScenario, sampleNumber int, events []traceEvent) error {
	if *performanceTraceDir == "" {
		return nil
	}
	tracePath := filepath.Join(*performanceTraceDir, fmt.Sprintf("%s-%03d.trace.jsonl", scenario.name, sampleNumber))
	if err := writeDedicatedTrace(tracePath, events); err != nil {
		return fmt.Errorf("write dedicated trace: %w", err)
	}
	return nil
}

type performanceMarker struct {
	Start         int64 `json:"start"`
	ActionWritten int64 `json:"action_written,omitempty"`
	Reaped        int64 `json:"reaped"`
}

func dedicatedDuration(events []traceEvent, markerPath, action string) (time.Duration, error) {
	if action == "" {
		measurement, err := parseDedicatedTraceSample(events, true)
		return measurement.StartupDuration, err
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return 0, err
	}
	var marker performanceMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return 0, err
	}
	if action == "event" {
		receipt, err := traceTime(events, "callback.event")
		if err != nil {
			return 0, err
		}
		if marker.ActionWritten == 0 || marker.Reaped < marker.ActionWritten {
			return 0, errors.New("callback action-write/reap markers are incomplete")
		}
		return time.Unix(0, marker.ActionWritten).Sub(receipt), nil
	}
	dispatch, err := traceTime(events, "preview.dispatch")
	if err != nil {
		return 0, err
	}
	return dispatch.Sub(time.Unix(0, marker.Start)), nil
}

func traceTime(events []traceEvent, name string) (time.Time, error) {
	for _, event := range events {
		if event.Event == name {
			return time.Parse(time.RFC3339Nano, event.Time)
		}
	}
	return time.Time{}, fmt.Errorf("missing trace event %s", name)
}

func zoxideCounters(attempts, starts int) integrationpkg.BenchmarkCounters {
	maxLive := 0
	if starts > 0 {
		maxLive = 1
	}
	return integrationpkg.BenchmarkCounters{ZoxideAttempts: attempts, ZoxideStarts: starts, ZoxideExits: starts,
		ZoxideMaxLive: maxLive, ZoxideProcesses: starts}
}

func enforceDedicatedGoals(t *testing.T, reports []integrationpkg.BenchmarkReport) {
	t.Helper()
	startup, navigation, preview := int64(150_000), int64(90_000), int64(20_000)
	if runtime.GOOS == "windows" {
		startup, navigation, preview = 275_000, 180_000, 50_000
	}
	for _, report := range reports {
		limit := int64(0)
		measuredP95 := report.P95US
		switch {
		case strings.HasPrefix(report.Scenario, "startup-"):
			limit = startup
			if report.StartupDuration == nil {
				t.Errorf("%s has no StartupDuration report", report.Scenario)
				continue
			}
			measuredP95 = report.StartupDuration.P95US
		case report.Scenario == "navigation-local-only":
			limit = navigation
			if report.ActionDuration != nil {
				measuredP95 = report.ActionDuration.P95US
			}
		case report.Scenario == "preview-dispatch":
			limit = preview
			if report.ActionDuration != nil {
				measuredP95 = report.ActionDuration.P95US
			}
		}
		if limit > 0 && measuredP95 > limit {
			t.Errorf("%s p95=%dus goal=%dus", report.Scenario, measuredP95, limit)
		}
	}
}
