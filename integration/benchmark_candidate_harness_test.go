package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

type dedicatedCandidateScenario struct {
	name, policy, mode string
	picker             protocol.Picker
	generations        int
	timeout            time.Duration
	expected           *integrationpkg.BenchmarkCounters
}

func TestDedicatedCandidateScenarioCoverageAndExplicitCounters(t *testing.T) {
	want := []string{
		"candidate-initial-cached-overlap-10000",
		"candidate-timeout-discard",
		"candidate-cached-repeated",
		"candidate-fresh-repeated",
		"candidate-missing",
		"candidate-spawn-failure",
		"candidate-cp-cached",
		"candidate-cp-fresh",
	}
	scenarios := dedicatedCandidateScenarios(75 * time.Millisecond)
	if len(scenarios) != len(want) {
		t.Fatalf("scenarios=%d want %d", len(scenarios), len(want))
	}
	for index, name := range want {
		if scenarios[index].name != name || scenarios[index].expected == nil {
			t.Fatalf("scenario[%d]=%+v want %q with explicit counters", index, scenarios[index], name)
		}
	}
	for _, scenario := range scenarios[len(scenarios)-2:] {
		if *scenario.expected != (integrationpkg.BenchmarkCounters{}) {
			t.Fatalf("cp expected counters=%+v", *scenario.expected)
		}
	}
	for _, scenario := range scenarios[2:4] {
		if *scenario.expected != zoxideCounters(1, 1) {
			t.Fatalf("repeated CD expected counters=%+v", *scenario.expected)
		}
	}
}

func dedicatedCandidateScenarios(defaultTimeout time.Duration) []dedicatedCandidateScenario {
	return []dedicatedCandidateScenario{
		candidateScenario("candidate-initial-cached-overlap-10000", "cached", "records-10000", protocol.PickerCD, 1,
			time.Second, zoxideCounters(1, 1)),
		candidateScenario("candidate-timeout-discard", "cached", "timeout", protocol.PickerCD, 1,
			defaultTimeout, zoxideCounters(1, 1)),
		candidateScenario("candidate-cached-repeated", "cached", "present", protocol.PickerCD, 3,
			time.Second, zoxideCounters(1, 1)),
		candidateScenario("candidate-fresh-repeated", "fresh", "present", protocol.PickerCD, 3,
			time.Second, zoxideCounters(1, 1)),
		candidateScenario("candidate-missing", "cached", "missing", protocol.PickerCD, 1,
			time.Second, zoxideCounters(1, 0)),
		candidateScenario("candidate-spawn-failure", "cached", "spawn-failure", protocol.PickerCD, 1,
			time.Second, zoxideCounters(1, 0)),
		candidateScenario("candidate-cp-cached", "cached", "present", protocol.PickerCP, 3,
			time.Second, integrationpkg.BenchmarkCounters{}),
		candidateScenario("candidate-cp-fresh", "fresh", "present", protocol.PickerCP, 3,
			time.Second, integrationpkg.BenchmarkCounters{}),
	}
}

func candidateScenario(name, policy, mode string, picker protocol.Picker, generations int, timeout time.Duration,
	expected integrationpkg.BenchmarkCounters) dedicatedCandidateScenario {
	return dedicatedCandidateScenario{name: name, policy: policy, mode: mode, picker: picker, generations: generations,
		timeout: timeout, expected: &expected}
}

func runDedicatedCandidateReports(t *testing.T, samples int, metadata integrationpkg.BenchmarkMetadata,
	defaultTimeout time.Duration) []integrationpkg.BenchmarkReport {
	t.Helper()
	scenarios := dedicatedCandidateScenarios(defaultTimeout)
	reports := make([]integrationpkg.BenchmarkReport, 0, len(scenarios))
	for _, scenario := range scenarios {
		scenario := scenario
		report, err := integrationpkg.RunBenchmark(context.Background(), integrationpkg.BenchmarkOptions{
			Scenario: scenario.name, Samples: samples, Policy: scenario.policy, Timeout: scenario.timeout,
			Expected: scenario.expected, Metadata: metadata,
			Measure: func(context.Context) (integrationpkg.BenchmarkSample, error) {
				return measureDedicatedCandidateSample(t, scenario)
			},
		})
		if err != nil {
			t.Fatalf("scenario %s: %v", scenario.name, err)
		}
		reports = append(reports, report)
	}
	return reports
}

func measureDedicatedCandidateSample(t *testing.T, scenario dedicatedCandidateScenario) (integrationpkg.BenchmarkSample, error) {
	t.Helper()
	root := t.TempDir()
	counts := new(performanceProcessCounts)
	environment := append(os.Environ(), parityHelperEnvironment+"=performance", "GO_PERF_HELPER=zoxide",
		"GO_PERF_ZOXIDE_PATH="+root, "GO_PERF_ZOXIDE_MODE="+scenario.mode, "GORACE=atexit_sleep_ms=0")
	path := os.Args[0]
	if scenario.mode == "missing" {
		path = filepath.Join(root, "missing")
	} else if scenario.mode == "spawn-failure" {
		path = newSpawnFailureExecutable(t)
	}
	runner := process.Runner{Observe: counts.observe}
	newCache := func() (*candidate.ZoxideCache, error) {
		return candidate.NewZoxideCache(runner, path, environment, scenario.timeout)
	}
	builder := new(candidate.Builder)
	if scenario.policy == "cached" {
		cache, err := newCache()
		if err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
		builder.ConfigureCached(cache)
	} else {
		builder.ConfigureFresh(newCache)
	}
	started := time.Now()
	for generation := 0; generation < scenario.generations; generation++ {
		result, err := builder.Build(context.Background(), candidate.BuildRequest{Picker: scenario.picker,
			Location: pathutil.Filesystem([]byte(root)), Initial: generation == 0, StatWorkers: 2})
		if err != nil {
			return integrationpkg.BenchmarkSample{}, err
		}
		if generation == 0 {
			if expected, ok := expectedZoxideOutcome(scenario.mode); ok && result.Metrics.ZoxideOutcome != expected {
				return integrationpkg.BenchmarkSample{}, fmt.Errorf("mode %q outcome=%q; want %q", scenario.mode, result.Metrics.ZoxideOutcome, expected)
			}
		}
		if generation > 0 && (result.Metrics.ZoxideOutcome != "not-run" || result.Metrics.ZoxideAttempts != 0 ||
			result.Metrics.ZoxideStarts != 0 || result.Metrics.ZoxideExits != 0 || result.Metrics.ZoxideProcesses != 0 ||
			result.Metrics.ZoxideLive != 0 || result.Metrics.ZoxideMaxLive != 0) {
			return integrationpkg.BenchmarkSample{}, fmt.Errorf("generation %d metrics=%+v", generation+1, result.Metrics)
		}
		if scenario.mode == "records-10000" && len(result.Records) < 10_000 {
			return integrationpkg.BenchmarkSample{}, errors.New("10,000-row candidate fixture was not merged")
		}
	}
	duration := time.Since(started)
	attempts, starts, exits, live, maxLive := counts.values()
	counters := integrationpkg.BenchmarkCounters{ZoxideAttempts: attempts, ZoxideStarts: starts, ZoxideExits: exits,
		ZoxideProcesses: starts, ZoxideMaxLive: maxLive}
	if live != 0 {
		return integrationpkg.BenchmarkSample{}, errors.New("candidate scenario retained a live zoxide process")
	}
	// Candidate harness timings are source-cost measurements, not end-user
	// startup. Keep them out of StartupDuration so reports remain explicit.
	return integrationpkg.BenchmarkSample{SourceDuration: &duration, BenchmarkCounters: counters}, nil
}
