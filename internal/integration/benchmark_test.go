package integration

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBenchmarkDefaultsToExactlyFiftySamplesAndUsesNearestRank(t *testing.T) {
	calls := 0
	report, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "cached-navigation",
		Policy:   "cached",
		Timeout:  75 * time.Millisecond,
		Measure: func(context.Context) (BenchmarkSample, error) {
			calls++
			return BenchmarkSample{Duration: time.Duration(calls) * time.Microsecond, BenchmarkCounters: BenchmarkCounters{
				ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1, ZoxideMaxLive: 1, ZoxideProcesses: 1}}, nil
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
		Scenario: "cached-navigation", Samples: 2, Policy: "cached",
		Expected: &BenchmarkCounters{ZoxideAttempts: 1, ZoxideStarts: 1, ZoxideExits: 1, ZoxideMaxLive: 1, ZoxideProcesses: 1},
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{Duration: time.Microsecond, BenchmarkCounters: BenchmarkCounters{ZoxideAttempts: 1}}, nil
		},
	})
	if !errors.Is(err, ErrBenchmarkCounters) {
		t.Fatalf("error=%v", err)
	}
}

func TestBenchmarkEnforcesExplicitAllZeroCounters(t *testing.T) {
	expected := BenchmarkCounters{}
	_, err := RunBenchmark(context.Background(), BenchmarkOptions{
		Scenario: "cached-navigation", Samples: 1, Policy: "cached", Expected: &expected,
		Measure: func(context.Context) (BenchmarkSample, error) {
			return BenchmarkSample{Duration: time.Microsecond,
				BenchmarkCounters: BenchmarkCounters{ZoxideAttempts: 1}}, nil
		},
	})
	if !errors.Is(err, ErrBenchmarkCounters) {
		t.Fatalf("mutated all-zero counters error=%v", err)
	}
}

func TestBenchmarkRejectsUnboundedScenarioAndPolicyEnums(t *testing.T) {
	for _, options := range []BenchmarkOptions{
		{Scenario: "../../token-query-record", Measure: func(context.Context) (BenchmarkSample, error) { return BenchmarkSample{}, nil }},
		{Scenario: "cached-navigation", Policy: "attacker-controlled", Measure: func(context.Context) (BenchmarkSample, error) { return BenchmarkSample{}, nil }},
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
