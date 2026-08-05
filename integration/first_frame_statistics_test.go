package integration

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

type firstFramePair struct {
	disabled firstFrameSample
	enabled  firstFrameSample
}

type firstFramePercentiles struct {
	P50US int64
	P95US int64
}

type firstFrameInterval struct {
	LowerUS int64 `json:"lower"`
	UpperUS int64 `json:"upper"`
}

type firstFrameDistribution struct {
	firstFramePercentiles
	P50CI firstFrameInterval
	P95CI firstFrameInterval
}

type firstFrameDelta struct {
	P50US   int64
	P95US   int64
	LowerUS int64
	UpperUS int64
	P50CI   firstFrameInterval
	P95CI   firstFrameInterval
}

type firstFrameMetricReport struct {
	enabled         firstFrameDistribution
	disabled        firstFrameDistribution
	enabledPresent  bool
	disabledPresent bool
	delta           *firstFrameDelta
}

type firstFrameComparisonReport struct {
	metrics map[string]firstFrameMetricReport
}

func summarizeFirstFramePairs(pairs []firstFramePair, seed int64) (firstFrameComparisonReport, error) {
	return summarizeFirstFrameMetrics(pairs, seed, []string{"meaningful_frame"})
}

func summarizeAllFirstFramePairs(pairs []firstFramePair, seed int64) (firstFrameComparisonReport, error) {
	return summarizeFirstFrameMetrics(pairs, seed, []string{
		"first_byte", "meaningful_frame", "fzf_start", "zoxide_terminal", "preview_dispatch", "preview_complete",
		"callback_through_frame", "info_through_frame", "display_through_frame", "preview_through_frame",
		"event_through_frame", "load_through_frame", "callback_through_preview_complete", "renderer_eza_through_preview_complete",
		"sidecar_get_count", "sidecar_get_duration", "sidecar_post_count", "sidecar_post_duration",
	})
}

func summarizeFirstFrameMetrics(pairs []firstFramePair, seed int64, names []string) (firstFrameComparisonReport, error) {
	if len(pairs) == 0 {
		return firstFrameComparisonReport{}, errors.New("first-frame comparison has no pairs")
	}
	values := make(map[string][2][]int64)
	for _, pair := range pairs {
		for _, name := range names {
			current := values[name]
			disabled, disabledOK := firstFrameMetricValue(pair.disabled, name)
			enabled, enabledOK := firstFrameMetricValue(pair.enabled, name)
			if disabledOK {
				current[0] = append(current[0], disabled)
			}
			if enabledOK {
				current[1] = append(current[1], enabled)
			}
			values[name] = current
		}
	}
	report := firstFrameComparisonReport{metrics: make(map[string]firstFrameMetricReport, len(values))}
	for name, paired := range values {
		if len(paired[0]) == 0 && len(paired[1]) == 0 {
			continue
		}
		random := rand.New(rand.NewSource(seed + int64(len(name))))
		metric := firstFrameMetricReport{enabledPresent: len(paired[1]) != 0, disabledPresent: len(paired[0]) != 0}
		if metric.enabledPresent {
			metric.enabled = summarizeFirstFrameDistribution(paired[1], random)
		}
		if metric.disabledPresent {
			metric.disabled = summarizeFirstFrameDistribution(paired[0], random)
		}
		if metric.enabledPresent && metric.disabledPresent {
			if len(paired[0]) != len(paired[1]) {
				return firstFrameComparisonReport{}, fmt.Errorf("first-frame metric %s is not paired", name)
			}
			deltas := make([]int64, len(paired[0]))
			for index := range deltas {
				deltas[index] = paired[1][index] - paired[0][index]
			}
			delta := summarizeFirstFrameDelta(deltas, random)
			metric.delta = &delta
		}
		report.metrics[name] = metric
	}
	return report, nil
}

func firstFrameMetricValue(sample firstFrameSample, name string) (int64, bool) {
	switch name {
	case "first_byte":
		return int64(sample.metrics.firstByteUS), sample.metrics.firstBytePresent || sample.metrics.firstByteUS != 0
	case "meaningful_frame":
		return int64(sample.metrics.meaningfulFrameUS), sample.metrics.meaningfulFramePresent || sample.metrics.meaningfulFrameUS != 0
	case "fzf_start":
		return int64(sample.metrics.fzfStartUS), sample.metrics.fzfStartPresent || sample.metrics.fzfStartUS != 0
	case "zoxide_terminal":
		return int64(sample.metrics.zoxideTerminalUS), sample.metrics.zoxideTerminalPresent || sample.metrics.zoxideTerminalUS != 0
	case "preview_dispatch":
		return int64(sample.metrics.previewDispatchUS), sample.metrics.previewDispatchPresent || sample.metrics.previewDispatchUS != 0
	case "preview_complete":
		return int64(sample.metrics.previewCompleteUS), sample.metrics.previewCompletePresent || sample.metrics.previewCompleteUS != 0
	case "callback_through_frame":
		return int64(sample.callbackThroughFrame.callbackTotal()), true
	case "info_through_frame":
		return int64(sample.callbackThroughFrame.info), true
	case "display_through_frame":
		return int64(sample.callbackThroughFrame.display), true
	case "preview_through_frame":
		return int64(sample.callbackThroughFrame.preview), true
	case "event_through_frame":
		return int64(sample.callbackThroughFrame.event), true
	case "load_through_frame":
		return int64(sample.callbackThroughFrame.load), true
	case "callback_through_preview_complete":
		return int64(sample.callbackPreviewComplete.callbackTotal()), true
	case "renderer_eza_through_preview_complete":
		return int64(sample.processPreviewComplete.renderers["eza"]), true
	case "sidecar_get_count":
		if sample.sidecar == nil {
			return 0, false
		}
		return int64(sample.sidecar.getCount), true
	case "sidecar_get_duration":
		if sample.sidecar == nil {
			return 0, false
		}
		return sample.sidecar.getDurationUS, true
	case "sidecar_post_count":
		if sample.sidecar == nil {
			return 0, false
		}
		return int64(sample.sidecar.postCount), true
	case "sidecar_post_duration":
		if sample.sidecar == nil {
			return 0, false
		}
		return sample.sidecar.postDurationUS, true
	default:
		return 0, false
	}
}

func (counts firstFrameCallbackCounts) callbackTotal() int {
	return counts.info + counts.display + counts.preview + counts.event + counts.load
}

func summarizeFirstFrameDistribution(values []int64, random *rand.Rand) firstFrameDistribution {
	return firstFrameDistribution{
		firstFramePercentiles: firstFramePercentiles{P50US: firstFrameNearestRank(values, 0.50), P95US: firstFrameNearestRank(values, 0.95)},
		P50CI:                 firstFrameBootstrapCI(values, 0.50, random), P95CI: firstFrameBootstrapCI(values, 0.95, random),
	}
}

func summarizeFirstFrameDelta(values []int64, random *rand.Rand) firstFrameDelta {
	p50 := firstFrameBootstrapCI(values, 0.50, random)
	return firstFrameDelta{P50US: firstFrameNearestRank(values, 0.50), P95US: firstFrameNearestRank(values, 0.95),
		LowerUS: p50.LowerUS, UpperUS: p50.UpperUS, P50CI: p50, P95CI: firstFrameBootstrapCI(values, 0.95, random)}
}

func firstFrameNearestRank(values []int64, percentile float64) int64 {
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	index := int(math.Ceil(float64(len(sorted)) * percentile))
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

func firstFrameBootstrapCI(values []int64, percentile float64, random *rand.Rand) firstFrameInterval {
	const iterations = 2_000
	statistics := make([]int64, iterations)
	resample := make([]int64, len(values))
	for iteration := range statistics {
		for index := range resample {
			resample[index] = values[random.Intn(len(values))]
		}
		statistics[iteration] = firstFrameNearestRank(resample, percentile)
	}
	sort.Slice(statistics, func(left, right int) bool { return statistics[left] < statistics[right] })
	return firstFrameInterval{LowerUS: statistics[iterations*25/1000], UpperUS: statistics[iterations*975/1000]}
}
