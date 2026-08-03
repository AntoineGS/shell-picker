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
	"strings"
	"time"
)

const DefaultBenchmarkSamples = 50
const BenchmarkReportSchema = 2

var ErrBenchmarkCounters = errors.New("benchmark: process counters do not match")
var ErrBenchmarkMeasurement = errors.New("benchmark: duration measurement is inconsistent")

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
	// Duration is the legacy primary measurement. New callers should populate
	// StartupDuration or SourceDuration so the report cannot confuse user
	// startup with candidate/source cost.
	Duration time.Duration
	// StartupDuration is the user-visible interval from session.start to
	// fzf.start in a dedicated picker session. Nil means absent; a pointer to
	// zero means an immediate measurement.
	StartupDuration *time.Duration
	// EnrichmentDuration is from session.start to the terminal
	// zoxide.enrichment event. A nil pointer means no terminal was observed;
	// a non-nil pointer to zero means completion was immediate.
	EnrichmentDuration *time.Duration
	// LifecycleDuration is the full session lifecycle interval. It is kept
	// separate from StartupDuration because it can include asynchronous source
	// completion and shutdown/join work. Nil means absent; zero is valid.
	LifecycleDuration *time.Duration
	// SourceDuration is the candidate-only/source-cost measurement. It is not a
	// user startup measurement. Nil means absent; zero is valid.
	SourceDuration *time.Duration
	// ActionDuration is the user action measurement. Nil means absent; zero is
	// valid. Duration remains the legacy fallback for old callers.
	ActionDuration *time.Duration
	BenchmarkCounters
}

type BenchmarkPercentiles struct {
	P50US int64 `json:"p50_us"`
	P95US int64 `json:"p95_us"`
	P99US int64 `json:"p99_us"`
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
	Schema   int    `json:"schema"`
	Scenario string `json:"scenario"`
	Policy   string `json:"policy,omitempty"`
	Timeout  string `json:"timeout"`
	Samples  int    `json:"samples"`
	// DurationKind names the legacy p50_us/p95_us/p99_us aliases. The named
	// duration object below is authoritative for new consumers.
	DurationKind string `json:"duration_kind"`
	P50US        int64  `json:"p50_us"`
	P95US        int64  `json:"p95_us"`
	P99US        int64  `json:"p99_us"`
	NearestRank  string `json:"nearest_rank"`

	StartupDuration    *BenchmarkPercentiles `json:"startup_duration,omitempty"`
	EnrichmentDuration *BenchmarkPercentiles `json:"enrichment_duration,omitempty"`
	LifecycleDuration  *BenchmarkPercentiles `json:"lifecycle_duration,omitempty"`
	SourceDuration     *BenchmarkPercentiles `json:"source_duration,omitempty"`
	ActionDuration     *BenchmarkPercentiles `json:"action_duration,omitempty"`

	Counters BenchmarkCounters  `json:"counters"`
	Metadata *BenchmarkMetadata `json:"metadata,omitempty"`
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
	durationKind := benchmarkDurationKind(options.Scenario)
	durations := make([]time.Duration, 0, samples)
	enrichmentDurations := make([]time.Duration, 0, samples)
	enrichmentPresent := false
	lifecycleDurations := make([]time.Duration, 0, samples)
	lifecyclePresent := false
	primaryPresent := false
	var counters BenchmarkCounters
	for index := 0; index < samples; index++ {
		if cause := context.Cause(ctx); cause != nil {
			return BenchmarkReport{}, cause
		}
		sample, err := options.Measure(ctx)
		if err != nil {
			return BenchmarkReport{}, fmt.Errorf("benchmark %s sample %d: %w", options.Scenario, index+1, err)
		}
		if !validBenchmarkSample(sample) {
			return BenchmarkReport{}, ErrBenchmarkMeasurement
		}
		if !validBenchmarkCounters(sample.BenchmarkCounters) {
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
		duration := primaryBenchmarkDuration(sample, durationKind)
		if duration < 0 {
			return BenchmarkReport{}, ErrBenchmarkMeasurement
		}
		hasPrimary := benchmarkNamedDuration(sample, durationKind) != nil
		if index == 0 {
			primaryPresent = hasPrimary
		} else if hasPrimary != primaryPresent {
			return BenchmarkReport{}, ErrBenchmarkMeasurement
		}
		durations = append(durations, duration)

		hasEnrichment := sample.EnrichmentDuration != nil
		if index == 0 {
			enrichmentPresent = hasEnrichment
		} else if hasEnrichment != enrichmentPresent {
			return BenchmarkReport{}, ErrBenchmarkMeasurement
		}
		if hasEnrichment {
			enrichmentDurations = append(enrichmentDurations, *sample.EnrichmentDuration)
		}

		hasLifecycle := sample.LifecycleDuration != nil
		if index == 0 {
			lifecyclePresent = hasLifecycle
		} else if hasLifecycle != lifecyclePresent {
			return BenchmarkReport{}, ErrBenchmarkMeasurement
		}
		if hasLifecycle {
			lifecycleDurations = append(lifecycleDurations, *sample.LifecycleDuration)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	primary := benchmarkPercentiles(durations)
	report := BenchmarkReport{Schema: BenchmarkReportSchema, Scenario: options.Scenario, Policy: options.Policy,
		Timeout: options.Timeout.String(), Samples: samples, DurationKind: durationKind, NearestRank: "ceil(p*n)", Counters: counters,
		P50US: primary.P50US, P95US: primary.P95US, P99US: primary.P99US}
	switch durationKind {
	case "startup":
		report.StartupDuration = &primary
	case "source":
		report.SourceDuration = &primary
	default:
		report.ActionDuration = &primary
	}
	if enrichmentPresent {
		sort.Slice(enrichmentDurations, func(i, j int) bool { return enrichmentDurations[i] < enrichmentDurations[j] })
		value := benchmarkPercentiles(enrichmentDurations)
		report.EnrichmentDuration = &value
	}
	if lifecyclePresent {
		sort.Slice(lifecycleDurations, func(i, j int) bool { return lifecycleDurations[i] < lifecycleDurations[j] })
		value := benchmarkPercentiles(lifecycleDurations)
		report.LifecycleDuration = &value
	}
	if options.Metadata != (BenchmarkMetadata{}) {
		metadata := options.Metadata
		report.Metadata = &metadata
	}
	return report, nil
}

func validBenchmarkScenario(value string) bool {
	switch value {
	case "startup-local-only", "startup-zoxide-present", "startup-zoxide-missing", "startup-zoxide-spawn-failure", "startup-zoxide-blocked",
		"startup-zoxide-timeout", "navigation-local-only", "preview-dispatch",
		"candidate-initial-cached-overlap-10000", "candidate-timeout-discard", "candidate-cached-repeated",
		"candidate-fresh-repeated", "candidate-missing", "candidate-spawn-failure", "candidate-cp-cached", "candidate-cp-fresh":
		return true
	default:
		return false
	}
}

func benchmarkDurationKind(scenario string) string {
	if strings.HasPrefix(scenario, "candidate-") {
		return "source"
	}
	if strings.HasPrefix(scenario, "startup-") {
		return "startup"
	}
	return "action"
}

func primaryBenchmarkDuration(sample BenchmarkSample, durationKind string) time.Duration {
	if duration := benchmarkNamedDuration(sample, durationKind); duration != nil {
		return *duration
	}
	return sample.Duration
}

func benchmarkNamedDuration(sample BenchmarkSample, durationKind string) *time.Duration {
	switch durationKind {
	case "startup":
		return sample.StartupDuration
	case "source":
		return sample.SourceDuration
	default:
		return sample.ActionDuration
	}
}

func validBenchmarkSample(value BenchmarkSample) bool {
	if value.Duration < 0 || value.StartupDuration != nil && *value.StartupDuration < 0 ||
		value.LifecycleDuration != nil && *value.LifecycleDuration < 0 || value.SourceDuration != nil && *value.SourceDuration < 0 ||
		value.ActionDuration != nil && *value.ActionDuration < 0 {
		return false
	}
	return value.EnrichmentDuration == nil || *value.EnrichmentDuration >= 0
}

func benchmarkPercentiles(sorted []time.Duration) BenchmarkPercentiles {
	return BenchmarkPercentiles{P50US: nearestRank(sorted, 0.50).Microseconds(),
		P95US: nearestRank(sorted, 0.95).Microseconds(), P99US: nearestRank(sorted, 0.99).Microseconds()}
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
