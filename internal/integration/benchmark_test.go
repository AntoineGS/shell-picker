package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func durationPointer(value time.Duration) *time.Duration {
	return &value
}

func TestBenchmarkReportsNamedStartupEnrichmentAndLifecycleDurations(t *testing.T) {
	calls := 0
	report, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "startup-zoxide-present", Samples: 2, Policy: "cached", Timeout: time.Second,
		Measure: func(context.Context) (BenchmarkSample, error) {
			calls++
			return BenchmarkSample{
				StartupDuration:    durationPointer(time.Duration(calls*10) * time.Microsecond),
				EnrichmentDuration: durationPointer(time.Duration(calls*30) * time.Microsecond),
				LifecycleDuration:  durationPointer(time.Duration(calls*40) * time.Microsecond),
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != BenchmarkReportSchema || report.DurationKind != "startup" || report.P50US != 10 || report.P95US != 20 || report.P99US != 20 {
		t.Fatalf("legacy startup aliases=%+v", report)
	}
	if report.StartupDuration == nil || report.StartupDuration.P50US != 10 || report.StartupDuration.P95US != 20 {
		t.Fatalf("startup duration=%+v", report.StartupDuration)
	}
	if report.EnrichmentDuration == nil || report.EnrichmentDuration.P50US != 30 || report.EnrichmentDuration.P95US != 60 {
		t.Fatalf("enrichment duration=%+v", report.EnrichmentDuration)
	}
	if report.LifecycleDuration == nil || report.LifecycleDuration.P50US != 40 || report.LifecycleDuration.P95US != 80 {
		t.Fatalf("lifecycle duration=%+v", report.LifecycleDuration)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, required := range []string{`"duration_kind":"startup"`, `"startup_duration"`, `"enrichment_duration"`, `"lifecycle_duration"`, `"p50_us":10`} {
		if !strings.Contains(jsonText, required) {
			t.Fatalf("report JSON lacks %q: %s", required, jsonText)
		}
	}
}

func TestBenchmarkRepresentsAbsentEnrichmentSeparatelyFromZero(t *testing.T) {
	report, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "startup-local-only", Samples: 1,
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{Duration: 7 * time.Microsecond}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.EnrichmentDuration != nil {
		t.Fatalf("absent enrichment reported as duration=%+v", report.EnrichmentDuration)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"enrichment_duration"`) {
		t.Fatalf("absent enrichment serialized: %s", encoded)
	}

	immediate, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "startup-local-only", Samples: 1,
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{StartupDuration: durationPointer(0), EnrichmentDuration: durationPointer(0)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if immediate.EnrichmentDuration == nil || immediate.EnrichmentDuration.P50US != 0 {
		t.Fatalf("immediate enrichment=%+v", immediate.EnrichmentDuration)
	}
	immediateJSON, err := json.Marshal(immediate)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(immediateJSON), `"enrichment_duration"`) {
		t.Fatalf("immediate enrichment omitted: %s", immediateJSON)
	}
}

func TestCandidateBenchmarkDurationIsNamedSourceCost(t *testing.T) {
	report, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "candidate-missing", Samples: 1,
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{SourceDuration: durationPointer(17 * time.Microsecond)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.DurationKind != "source" || report.SourceDuration == nil || report.SourceDuration.P50US != 17 || report.StartupDuration != nil {
		t.Fatalf("source report=%+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"startup_duration"`) || !strings.Contains(string(encoded), `"source_duration"`) {
		t.Fatalf("source report JSON=%s", encoded)
	}
}

func TestBenchmarkRejectsMixedEnrichmentPresence(t *testing.T) {
	calls := 0
	_, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "startup-zoxide-present", Samples: 2,
		Measure: func(context.Context) (BenchmarkSample, error) {
			calls++
			if calls == 1 {
				return BenchmarkSample{StartupDuration: durationPointer(time.Microsecond), EnrichmentDuration: durationPointer(time.Microsecond)}, nil
			}
			return BenchmarkSample{StartupDuration: durationPointer(time.Microsecond)}, nil
		},
	})
	if !errors.Is(err, ErrBenchmarkMeasurement) {
		t.Fatalf("mixed enrichment error=%v", err)
	}
}

func TestBenchmarkAggregatesPresentZeroDurations(t *testing.T) {
	calls := 0
	report, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "startup-zoxide-present", Samples: 2,
		Measure: func(context.Context) (BenchmarkSample, error) {
			calls++
			if calls == 1 {
				return BenchmarkSample{StartupDuration: durationPointer(0), EnrichmentDuration: durationPointer(0), LifecycleDuration: durationPointer(0)}, nil
			}
			return BenchmarkSample{StartupDuration: durationPointer(4 * time.Microsecond), EnrichmentDuration: durationPointer(3 * time.Microsecond), LifecycleDuration: durationPointer(2 * time.Microsecond)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.StartupDuration == nil || report.StartupDuration.P50US != 0 || report.StartupDuration.P95US != 4 {
		t.Fatalf("startup=%+v", report.StartupDuration)
	}
	if report.EnrichmentDuration == nil || report.EnrichmentDuration.P50US != 0 || report.EnrichmentDuration.P95US != 3 {
		t.Fatalf("enrichment=%+v", report.EnrichmentDuration)
	}
	if report.LifecycleDuration == nil || report.LifecycleDuration.P50US != 0 || report.LifecycleDuration.P95US != 2 {
		t.Fatalf("lifecycle=%+v", report.LifecycleDuration)
	}
	encoded, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(encoded), `"lifecycle_duration":{"p50_us":0`) {
		t.Fatalf("lifecycle zero JSON=%s err=%v", encoded, err)
	}
}

func TestBenchmarkNamedZeroDoesNotFallBackToLegacyDuration(t *testing.T) {
	report, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "startup-local-only", Samples: 1,
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{Duration: 9 * time.Microsecond, StartupDuration: durationPointer(0)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.P50US != 0 || report.StartupDuration == nil || report.StartupDuration.P50US != 0 {
		t.Fatalf("report=%+v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(encoded), `"startup_duration":{"p50_us":0`) {
		t.Fatalf("startup zero JSON=%s err=%v", encoded, err)
	}
}

func TestBenchmarkNamedActionAndSourceZeroDoNotFallBack(t *testing.T) {
	t.Run("action", func(t *testing.T) {
		report, err := RunBenchmark(context.Background(), BenchmarkOptions{
			Scenario: "navigation-local-only", Samples: 1,
			Measure: func(context.Context) (BenchmarkSample, error) {
				return BenchmarkSample{Duration: 9 * time.Microsecond, ActionDuration: durationPointer(0)}, nil
			},
		})
		if err != nil || report.P50US != 0 || report.ActionDuration == nil || report.ActionDuration.P50US != 0 {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		encoded, marshalErr := json.Marshal(report)
		if marshalErr != nil || !strings.Contains(string(encoded), `"action_duration":{"p50_us":0`) {
			t.Fatalf("action zero JSON=%s err=%v", encoded, marshalErr)
		}
	})
	t.Run("source", func(t *testing.T) {
		report, err := RunBenchmark(context.Background(), BenchmarkOptions{
			Scenario: "candidate-missing", Samples: 1,
			Measure: func(context.Context) (BenchmarkSample, error) {
				return BenchmarkSample{Duration: 9 * time.Microsecond, SourceDuration: durationPointer(0)}, nil
			},
		})
		if err != nil || report.P50US != 0 || report.SourceDuration == nil || report.SourceDuration.P50US != 0 {
			t.Fatalf("report=%+v err=%v", report, err)
		}
		encoded, marshalErr := json.Marshal(report)
		if marshalErr != nil || !strings.Contains(string(encoded), `"source_duration":{"p50_us":0`) {
			t.Fatalf("source zero JSON=%s err=%v", encoded, marshalErr)
		}
	})
}

func TestBenchmarkRejectsMixedPrimaryMetricPresence(t *testing.T) {
	calls := 0
	_, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "startup-local-only", Samples: 2,
		Measure: func(context.Context) (BenchmarkSample, error) {
			calls++
			if calls == 1 {
				return BenchmarkSample{StartupDuration: durationPointer(time.Microsecond)}, nil
			}
			return BenchmarkSample{Duration: time.Microsecond}, nil
		},
	})
	if !errors.Is(err, ErrBenchmarkMeasurement) {
		t.Fatalf("mixed primary presence error=%v", err)
	}
}

func TestBenchmarkDefaultsToExactlyFiftySamplesAndUsesNearestRank(t *testing.T) {
	calls := 0
	report, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "navigation-local-only",
		Policy:   "cached",
		Timeout:  75 * time.Millisecond,
		Measure: func(context.Context) (BenchmarkSample, error) {
			calls++
			return BenchmarkSample{Duration: time.Duration(calls) * time.Microsecond}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 50 || report.Samples != 50 || report.P50US != 25 || report.P95US != 48 || report.P99US != 50 || report.NearestRank != "ceil(p*n)" {
		t.Fatalf("calls=%d report=%+v", calls, report)
	}
}

func TestBenchmarkRejectsCounterDriftInEverySample(t *testing.T) {
	_, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "navigation-local-only", Samples: 2, Policy: "cached",
		Expected: &BenchmarkCounters{},
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{Duration: time.Microsecond, BenchmarkCounters: BenchmarkCounters{
				ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1, ZoxideMaxLive: 1, ZoxideProcesses: 1}}, nil
		},
	})
	if !errors.Is(err, ErrBenchmarkCounters) {
		t.Fatalf("error=%v", err)
	}
}

func TestBenchmarkEnforcesExplicitAllZeroCounters(t *testing.T) {
	expected := BenchmarkCounters{}
	_, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "navigation-local-only", Samples: 1, Policy: "cached", Expected: &expected,
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{Duration: time.Microsecond,
				BenchmarkCounters: BenchmarkCounters{
					ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1, ZoxideMaxLive: 1, ZoxideProcesses: 1}}, nil
		},
	})
	if !errors.Is(err, ErrBenchmarkCounters) {
		t.Fatalf("mutated all-zero counters error=%v", err)
	}
}

func TestBenchmarkRejectsUnboundedScenarioAndPolicyEnums(t *testing.T) {
	for _, options := range []BenchmarkOptions{
		{Scenario: "../../token-query-record", Measure: func(context.Context) (BenchmarkSample, error) { return BenchmarkSample{}, nil }},
		{Scenario: "navigation-local-only", Policy: "attacker-controlled", Measure: func(context.Context) (BenchmarkSample, error) { return BenchmarkSample{}, nil }},
		{Scenario: "cached-navigation", Measure: func(context.Context) (BenchmarkSample, error) { return BenchmarkSample{}, nil }},
		{Scenario: "fresh-navigation", Measure: func(context.Context) (BenchmarkSample, error) { return BenchmarkSample{}, nil }},
		{Scenario: "fresh-exact-parity-navigation", Measure: func(context.Context) (BenchmarkSample, error) { return BenchmarkSample{}, nil }},
	} {
		if _, err := RunBenchmark(context.Background(), options); err == nil {
			t.Fatalf("invalid options accepted: %+v", options)
		}
	}
}

func TestBenchmarkQualificationRequiresFingerprintAndStableBaselines(t *testing.T) {
	metadata := BenchmarkMetadata{OS: "linux", Arch: "amd64", Hostname: "host", CPUModel: "cpu", CPUCount: 8,
		Power: "ac", Filesystem: "ext4", Terminal: "pty", GoVersion: "go1.26.5", FZFVersion: "0.74.1"}
	stable := []BaselineMetric{
		{Name: "child-spawn", MeanUS: 100, StdDevUS: 10},
		{Name: "loopback-http", MeanUS: 50, StdDevUS: 5},
		{Name: "warm-readdir-1000", MeanUS: 200, StdDevUS: 20},
	}
	baseline := HostBaseline{Fingerprint: MetadataFingerprint(metadata), Metrics: stable}
	if status := QualifyBaseline(metadata, baseline); status != "qualified" {
		t.Fatalf("stable status=%q", status)
	}
	baseline.Metrics[0].StdDevUS = 16
	if status := QualifyBaseline(metadata, baseline); status != "baseline-required" {
		t.Fatalf("unstable status=%q", status)
	}
	baseline.Metrics[0].StdDevUS = 10
	metadata.Power = "battery"
	if status := QualifyBaseline(metadata, baseline); status != "baseline-required" {
		t.Fatalf("mismatch status=%q", status)
	}
}
