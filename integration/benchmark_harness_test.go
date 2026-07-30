package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

var (
	performanceBinary   = flag.String("binary", "", "prebuilt shell-picker binary")
	performanceSamples  = flag.Int("samples", 50, "warm sample count")
	performanceOutput   = flag.String("output", "performance.json", "JSON output path")
	performanceBaseline = flag.String("baseline", "host-baseline.json", "host baseline path")
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

func TestDedicatedTraceCountersRequireMeasuredExitAndNoLiveRemainder(t *testing.T) {
	valid := []traceEvent{{Event: "generation.publish", ZoxideAttempts: 1, ZoxideStarts: 1,
		ZoxideExits: 1, ZoxideProcesses: 1, ZoxideLive: 0, ZoxideMaxLive: 1}}
	counters, err := traceBenchmarkCounters(valid)
	if err != nil || counters.ZoxideExits != 1 || counters.ZoxideProcesses != 1 {
		t.Fatalf("valid counters=%+v err=%v", counters, err)
	}
	mutated := []traceEvent{{Event: "generation.publish", ZoxideAttempts: 1, ZoxideStarts: 1,
		ZoxideExits: 0, ZoxideProcesses: 1, ZoxideLive: 1, ZoxideMaxLive: 1}}
	if _, err := traceBenchmarkCounters(mutated); err == nil {
		t.Fatal("missing measured exit and live remainder accepted")
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
	expected                         integrationpkg.BenchmarkCounters
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
	defaultTimeout := 75 * time.Millisecond
	if runtime.GOOS == "windows" {
		defaultTimeout = 150 * time.Millisecond
	}
	scenarios := []dedicatedScenario{
		{name: "startup-local-only", policy: "cached", zoxideMode: "empty", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1)},
		{name: "startup-zoxide-present", policy: "cached", zoxideMode: "present", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1)},
		{name: "startup-zoxide-missing", policy: "cached", zoxideMode: "missing", timeout: defaultTimeout,
			expected: zoxideCounters(1, 0)},
		{name: "startup-zoxide-spawn-failure", policy: "cached", zoxideMode: "spawn-failure", timeout: defaultTimeout,
			expected: zoxideCounters(1, 0)},
		{name: "startup-zoxide-timeout", policy: "cached", zoxideMode: "timeout", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1)},
		{name: "cached-navigation", policy: "cached", zoxideMode: "present", action: "event", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1)},
		{name: "fresh-navigation", policy: "fresh", zoxideMode: "present", action: "event", timeout: defaultTimeout,
			expected: zoxideCounters(2, 2)},
		{name: "fresh-exact-parity-navigation", policy: "fresh", zoxideMode: "present", action: "event", timeout: 0,
			expected: zoxideCounters(2, 2)},
		{name: "preview-dispatch", policy: "cached", zoxideMode: "present", action: "preview", timeout: defaultTimeout,
			expected: zoxideCounters(1, 1)},
	}
	reports := make([]integrationpkg.BenchmarkReport, 0, len(scenarios))
	for _, scenario := range scenarios {
		scenario := scenario
		report, err := integrationpkg.RunBenchmark(context.Background(), integrationpkg.BenchmarkOptions{
			Scenario: scenario.name, Samples: *performanceSamples, Policy: scenario.policy, Timeout: scenario.timeout,
			Expected: &scenario.expected, Metadata: metadata,
			Measure: func(context.Context) (integrationpkg.BenchmarkSample, error) {
				return runDedicatedPickerSample(t, binary, scenario)
			},
		})
		if err != nil {
			t.Fatalf("scenario %s: %v", scenario.name, err)
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

func requireDedicatedPerformance(t *testing.T) {
	t.Helper()
	if os.Getenv("SHELL_PICKER_DEDICATED_PERF") != "1" {
		t.Skip("set SHELL_PICKER_DEDICATED_PERF=1 for metadata-qualified dedicated measurement")
	}
	if err := validateDedicatedOptions(*performanceBinary, *performanceSamples); err != nil {
		t.Fatal(err)
	}
}

func runDedicatedPickerSample(t *testing.T, binary string, scenario dedicatedScenario) (integrationpkg.BenchmarkSample, error) {
	t.Helper()
	root := t.TempDir()
	for index := 0; index < 32; index++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("entry-%02d", index)), 0o700); err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
	}
	marker := filepath.Join(root, "marker.json")
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
		if err := os.Mkdir(zoxidePath, 0o700); err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
	} else if scenario.zoxideMode != "missing" {
		if err := copyExecutable(harness, zoxidePath); err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
	}
	environment := replaceEnvironment(os.Environ(), parityHelperEnvironment+"=performance", "GO_PERF_HELPER=fzf", "GO_PERF_ZOXIDE_MODE="+scenario.zoxideMode,
		"GO_PERF_ZOXIDE_PATH="+root, "GO_PERF_FZF_ACTION="+scenario.action, "GO_PERF_MARKER="+marker,
		"TERM=xterm-256color", "XDG_CACHE_HOME="+filepath.Join(root, "cache"), "LOCALAPPDATA="+filepath.Join(root, "cache"))
	args := []string{"cd", "--cwd", root, "--home", root, "--fzf", harness, "--zoxide-policy", scenario.policy,
		"--zoxide-timeout", scenario.timeout.String()}
	environment = replaceEnvironment(environment, "PATH="+tools+string(os.PathListSeparator)+filepath.Dir(binary))
	term := newTerminalSession(t, terminalConfig{Path: binary, Args: args, Environment: environment,
		Directory: root, Columns: 120, Lines: 35})
	defer term.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := term.Wait(ctx); err != nil {
		events := term.TraceEvents()
		summary := make([]string, 0, len(events))
		for _, event := range events {
			summary = append(summary, event.Event+":"+event.Outcome)
		}
		return integrationpkg.BenchmarkSample{}, fmt.Errorf("picker wait: %w; trace=%v", err, summary)
	}
	events := term.TraceEvents()
	if countTraceEvents(events, "fzf.start") != 1 {
		return integrationpkg.BenchmarkSample{}, errors.New("dedicated sample did not start exactly one fzf")
	}
	if countTraceEvents(events, "session.close") != 1 {
		return integrationpkg.BenchmarkSample{}, errors.New("dedicated sample did not close session")
	}
	counters, err := traceBenchmarkCounters(events)
	if err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	duration, err := dedicatedDuration(events, marker, scenario.action)
	if err != nil {
		return integrationpkg.BenchmarkSample{}, err
	}
	return integrationpkg.BenchmarkSample{Duration: duration, BenchmarkCounters: counters}, nil
}

func copyExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	return errors.Join(copyErr, closeErr)
}

func countTraceEvents(events []traceEvent, name string) int {
	count := 0
	for _, event := range events {
		if event.Event == name {
			count++
		}
	}
	return count
}

func traceBenchmarkCounters(events []traceEvent) (integrationpkg.BenchmarkCounters, error) {
	counters := integrationpkg.BenchmarkCounters{}
	for _, event := range events {
		if event.Event == "generation.publish" {
			if event.ZoxideExits != event.ZoxideStarts || event.ZoxideProcesses != event.ZoxideStarts || event.ZoxideLive != 0 {
				return integrationpkg.BenchmarkCounters{}, errors.New("zoxide trace has unmatched process lifecycle")
			}
			counters.ZoxideAttempts += event.ZoxideAttempts
			counters.ZoxideStarts += event.ZoxideStarts
			counters.ZoxideExits += event.ZoxideExits
			counters.ZoxideProcesses += event.ZoxideProcesses
			counters.ZoxideMaxLive = max(counters.ZoxideMaxLive, event.ZoxideMaxLive)
		}
		if event.Event == "preview.finished" {
			counters.PreviewStarts += event.ChildStarts
			counters.PreviewMaxLive = max(counters.PreviewMaxLive, event.MaxLiveChildren)
		}
	}
	return counters, nil
}

type performanceMarker struct {
	Start         int64 `json:"start"`
	ActionWritten int64 `json:"action_written,omitempty"`
	Reaped        int64 `json:"reaped"`
}

func dedicatedDuration(events []traceEvent, markerPath, action string) (time.Duration, error) {
	if action == "" {
		start, err := traceTime(events, "session.start")
		if err != nil {
			return 0, err
		}
		end, err := traceTime(events, "fzf.start")
		return end.Sub(start), err
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
		switch {
		case strings.HasPrefix(report.Scenario, "startup-"):
			limit = startup
		case report.Scenario == "cached-navigation":
			limit = navigation
		case report.Scenario == "preview-dispatch":
			limit = preview
		}
		if limit > 0 && report.P95US > limit {
			t.Errorf("%s p95=%dus goal=%dus", report.Scenario, report.P95US, limit)
		}
	}
}

func runPerformanceHelper() (int, bool) {
	if len(os.Args) == 3 && os.Args[1] == "query" && os.Args[2] == "--list" && os.Getenv("GO_PERF_HELPER") != "" {
		switch os.Getenv("GO_PERF_ZOXIDE_MODE") {
		case "timeout":
			signals := make(chan os.Signal, 1)
			signal.Notify(signals, os.Interrupt)
			<-signals
		case "empty":
		case "present", "":
			_, _ = fmt.Fprintln(os.Stdout, os.Getenv("GO_PERF_ZOXIDE_PATH"))
		case "records-10000":
			root := os.Getenv("GO_PERF_ZOXIDE_PATH")
			for index := range 10_000 {
				_, _ = fmt.Fprintf(os.Stdout, "%s%cbench-%05d\n", root, os.PathSeparator, index)
			}
		default:
			return 2, true
		}
		return 0, true
	}
	switch os.Getenv("GO_PERF_HELPER") {
	case "fzf":
		data, _ := io.ReadAll(os.Stdin)
		action := os.Getenv("GO_PERF_FZF_ACTION")
		if action != "" {
			start := time.Now().UnixNano()
			commandName := performanceCallbackName(os.Args[1:])
			commandArg := "e:up"
			if action == "preview" {
				commandArg = "p"
			}
			current := data
			if index := bytes.IndexByte(current, 0); index >= 0 {
				current = current[:index]
			}
			command := exec.Command(commandName, "--fzf-shell", commandArg)
			command.Env = replaceEnvironment(os.Environ(), "FZF_KEY=left", "FZF_QUERY=", "FZF_CURRENT_ITEM="+string(current))
			command.Stderr = io.Discard
			marker := performanceMarker{Start: start}
			if err := runPerformanceCallback(command, action, &marker); err != nil {
				return 3, true
			}
			encoded, _ := json.Marshal(marker)
			if err := os.WriteFile(os.Getenv("GO_PERF_MARKER"), encoded, 0o600); err != nil {
				return 4, true
			}
		}
		_, _ = os.Stdout.Write([]byte{0})
		return 130, true
	default:
		return 0, false
	}
}

func runPerformanceCallback(command *exec.Cmd, action string, marker *performanceMarker) error {
	if action != "event" {
		command.Stdout = io.Discard
		err := command.Run()
		marker.Reaped = time.Now().UnixNano()
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		return err
	}
	buffer := make([]byte, 64<<10)
	written, readErr := stdout.Read(buffer)
	if written > 0 {
		marker.ActionWritten = time.Now().UnixNano()
	}
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := command.Wait()
	marker.Reaped = time.Now().UnixNano()
	if written == 0 {
		return errors.Join(errors.New("callback wrote no action"), readErr, drainErr, waitErr)
	}
	return errors.Join(readErr, drainErr, waitErr)
}

func performanceCallbackName(arguments []string) string {
	for _, argument := range arguments {
		if value, ok := strings.CutPrefix(argument, "--with-shell="); ok {
			name, _, _ := strings.Cut(value, " ")
			return name
		}
	}
	return "shell-picker"
}
