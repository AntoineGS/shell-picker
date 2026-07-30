package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

const DefaultBenchmarkSamples = 50

var ErrBenchmarkCounters = errors.New("benchmark: process counters do not match")

type BenchmarkCounters struct {
	ZoxideAttempts  int `json:"zoxide_attempts"`
	ZoxideStarts    int `json:"zoxide_starts"`
	ZoxideExits     int `json:"zoxide_exits"`
	ZoxideMaxLive   int `json:"zoxide_max_live"`
	ZoxideProcesses int `json:"zoxide_processes"`
	PreviewStarts   int `json:"preview_child_starts"`
	PreviewMaxLive  int `json:"preview_max_live"`
}

type BenchmarkSample struct {
	Duration time.Duration
	BenchmarkCounters
}

type BenchmarkOptions struct {
	Scenario string
	Samples  int
	Policy   string
	Timeout  time.Duration
	Expected *BenchmarkCounters
	Metadata BenchmarkMetadata
	Measure  func(context.Context) (BenchmarkSample, error)
}

type BenchmarkReport struct {
	Schema      int                `json:"schema"`
	Scenario    string             `json:"scenario"`
	Policy      string             `json:"policy,omitempty"`
	Timeout     string             `json:"timeout"`
	Samples     int                `json:"samples"`
	P50US       int64              `json:"p50_us"`
	P95US       int64              `json:"p95_us"`
	P99US       int64              `json:"p99_us"`
	NearestRank string             `json:"nearest_rank"`
	Counters    BenchmarkCounters  `json:"counters"`
	Metadata    *BenchmarkMetadata `json:"metadata,omitempty"`
}

type BenchmarkMetadata struct {
	Hostname    string `json:"hostname"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUModel    string `json:"cpu_model"`
	CPUCount    int    `json:"cpu_count"`
	CPUGovernor string `json:"cpu_governor,omitempty"`
	PowerPlan   string `json:"power_plan,omitempty"`
	Memory      string `json:"memory,omitempty"`
	Filesystem  string `json:"filesystem"`
	Terminal    string `json:"terminal"`
	FZFVersion  string `json:"fzf_version"`
	GoVersion   string `json:"go_version"`
	Antivirus   string `json:"antivirus,omitempty"`
	Power       string `json:"power"`
}

type BaselineMetric struct {
	Name     string  `json:"name"`
	MeanUS   float64 `json:"mean_us"`
	StdDevUS float64 `json:"stddev_us"`
}

type HostBaseline struct {
	Schema      int               `json:"schema"`
	Fingerprint string            `json:"fingerprint"`
	Metadata    BenchmarkMetadata `json:"metadata"`
	Metrics     []BaselineMetric  `json:"metrics"`
}

func RunBenchmark(ctx context.Context, options BenchmarkOptions) (BenchmarkReport, error) {
	if ctx == nil {
		return BenchmarkReport{}, errors.New("benchmark: nil context")
	}
	if !validBenchmarkScenario(options.Scenario) || options.Measure == nil || options.Samples < 0 || options.Timeout < 0 {
		return BenchmarkReport{}, errors.New("benchmark: invalid options")
	}
	if options.Policy != "" && options.Policy != "cached" && options.Policy != "fresh" {
		return BenchmarkReport{}, errors.New("benchmark: invalid policy")
	}
	samples := options.Samples
	if samples == 0 {
		samples = DefaultBenchmarkSamples
	}
	durations := make([]time.Duration, 0, samples)
	var counters BenchmarkCounters
	for index := 0; index < samples; index++ {
		if cause := context.Cause(ctx); cause != nil {
			return BenchmarkReport{}, cause
		}
		sample, err := options.Measure(ctx)
		if err != nil {
			return BenchmarkReport{}, fmt.Errorf("benchmark %s sample %d: %w", options.Scenario, index+1, err)
		}
		if sample.Duration < 0 || !validBenchmarkCounters(sample.BenchmarkCounters) {
			return BenchmarkReport{}, ErrBenchmarkCounters
		}
		if index == 0 {
			counters = sample.BenchmarkCounters
		} else if sample.BenchmarkCounters != counters {
			return BenchmarkReport{}, ErrBenchmarkCounters
		}
		if options.Expected != nil && sample.BenchmarkCounters != *options.Expected {
			return BenchmarkReport{}, ErrBenchmarkCounters
		}
		durations = append(durations, sample.Duration)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	report := BenchmarkReport{Schema: 1, Scenario: options.Scenario, Policy: options.Policy,
		Timeout: options.Timeout.String(), Samples: samples, NearestRank: "ceil(p*n)", Counters: counters,
		P50US: nearestRank(durations, 0.50).Microseconds(), P95US: nearestRank(durations, 0.95).Microseconds(),
		P99US: nearestRank(durations, 0.99).Microseconds()}
	if options.Metadata != (BenchmarkMetadata{}) {
		metadata := options.Metadata
		report.Metadata = &metadata
	}
	return report, nil
}

func validBenchmarkScenario(value string) bool {
	switch value {
	case "startup-local-only", "startup-zoxide-present", "startup-zoxide-missing", "startup-zoxide-spawn-failure",
		"startup-zoxide-timeout", "navigation-local-only", "preview-dispatch",
		"candidate-initial-cached-overlap-10000", "candidate-timeout-discard", "candidate-cached-repeated",
		"candidate-fresh-repeated", "candidate-missing", "candidate-spawn-failure", "candidate-cp-cached", "candidate-cp-fresh":
		return true
	default:
		return false
	}
}

func validBenchmarkCounters(value BenchmarkCounters) bool {
	values := []int{value.ZoxideAttempts, value.ZoxideStarts, value.ZoxideExits, value.ZoxideMaxLive,
		value.ZoxideProcesses, value.PreviewStarts, value.PreviewMaxLive}
	for _, count := range values {
		if count < 0 || count > 1_000_000 {
			return false
		}
	}
	return value.ZoxideStarts <= value.ZoxideAttempts && value.ZoxideExits == value.ZoxideStarts &&
		value.ZoxideProcesses == value.ZoxideStarts && value.ZoxideMaxLive <= value.ZoxideStarts &&
		value.PreviewMaxLive <= value.PreviewStarts
}

func nearestRank(sorted []time.Duration, percentile float64) time.Duration {
	index := int(math.Ceil(percentile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func MetadataFingerprint(metadata BenchmarkMetadata) string {
	encoded, _ := json.Marshal(metadata)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func QualifyBaseline(metadata BenchmarkMetadata, baseline HostBaseline) string {
	if baseline.Fingerprint == "" || baseline.Fingerprint != MetadataFingerprint(metadata) || len(baseline.Metrics) != 3 {
		return "baseline-required"
	}
	wanted := map[string]bool{"child-spawn": true, "loopback-http": true, "warm-readdir-1000": true}
	for _, metric := range baseline.Metrics {
		if !wanted[metric.Name] || metric.MeanUS <= 0 || metric.StdDevUS < 0 || metric.StdDevUS/metric.MeanUS > 0.15 {
			return "baseline-required"
		}
		delete(wanted, metric.Name)
	}
	if len(wanted) != 0 {
		return "baseline-required"
	}
	return "qualified"
}
