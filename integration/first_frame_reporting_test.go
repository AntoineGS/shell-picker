package integration

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	integrationpkg "github.com/AntoineGS/shell-picker/internal/integration"
)

type firstFrameSidecarJSON struct {
	GetCount       int64 `json:"get_count"`
	PostCount      int64 `json:"post_count"`
	GetDurationUS  int64 `json:"get_duration_us"`
	PostDurationUS int64 `json:"post_duration_us"`
}

func firstFrameMetricsJSONFor(metrics firstFrameMetrics) firstFrameMetricsJSON {
	return firstFrameMetricsJSON{FirstConPTYByteUS: int64(metrics.firstByteUS), MeaningfulFrameUS: int64(metrics.meaningfulFrameUS),
		FZFStartUS: int64(metrics.fzfStartUS), ZoxideTerminalUS: int64(metrics.zoxideTerminalUS),
		PreviewDispatchUS: int64(metrics.previewDispatchUS), PreviewCompleteUS: int64(metrics.previewCompleteUS)}
}

func firstFrameSidecarJSONFor(metrics *firstFrameSidecarMetrics) *firstFrameSidecarJSON {
	if metrics == nil {
		return nil
	}
	return &firstFrameSidecarJSON{GetCount: int64(metrics.getCount), PostCount: int64(metrics.postCount),
		GetDurationUS: metrics.getDurationUS, PostDurationUS: metrics.postDurationUS}
}

func firstFrameProcessJSONFor(counts firstFrameProcessCounts) firstFrameProcessJSON {
	renderers := make(map[string]int, len(counts.renderers))
	for name, count := range counts.renderers {
		renderers[name] = count
	}
	return firstFrameProcessJSON{Total: counts.total, Renderers: renderers}
}

func firstFrameCallbackJSONFor(counts firstFrameCallbackCounts) firstFrameCallbackJSON {
	return firstFrameCallbackJSON{Total: counts.callbackTotal(), Info: counts.info, Display: counts.display,
		Preview: counts.preview, Event: counts.event, Load: counts.load}
}

func firstFramePublicSampleFor(pair int, sample firstFrameSample) firstFramePublicSample {
	return firstFramePublicSample{Pair: pair, Mode: sample.mode, SidecarEnabled: sample.mode == firstFrameEnabled,
		CandidateCount: sample.candidateCount, Metrics: firstFrameMetricsJSONFor(sample.metrics),
		CallbackThroughFrame: firstFrameCallbackJSONFor(sample.callbackThroughFrame), CallbackPreviewComplete: firstFrameCallbackJSONFor(sample.callbackPreviewComplete),
		ProcessThroughFrame: firstFrameProcessJSONFor(sample.processThroughFrame), ProcessPreviewComplete: firstFrameProcessJSONFor(sample.processPreviewComplete),
		Sidecar: firstFrameSidecarJSONFor(sample.sidecar), RawArtifact: firstFrameRawPath(pair, sample.mode)}
}

func firstFrameRawPath(pair int, mode firstFrameMode) string {
	return firstFrameRelativeArtifactPath(*firstFrameRawDir, filepath.Join(*firstFrameRawDir, fmt.Sprintf("pair-%03d-%s.json", pair, mode)))
}

func firstFrameRelativeArtifactPath(root, artifact string) string {
	relative, err := filepath.Rel(root, artifact)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(relative)
}

func sanitizeFirstFrameMetadata(metadata integrationpkg.BenchmarkMetadata) integrationpkg.BenchmarkMetadata {
	metadata.Hostname = "sha256:" + fmt.Sprintf("%x", sha256.Sum256([]byte(metadata.Hostname)))
	metadata.CPUModel = redactFirstFrameUserPath(metadata.CPUModel)
	metadata.CPUGovernor = redactFirstFrameUserPath(metadata.CPUGovernor)
	metadata.PowerPlan = redactFirstFrameUserPath(metadata.PowerPlan)
	metadata.Memory = redactFirstFrameUserPath(metadata.Memory)
	metadata.Filesystem = redactFirstFrameUserPath(metadata.Filesystem)
	metadata.Terminal = redactFirstFrameUserPath(metadata.Terminal)
	metadata.FZFVersion = redactFirstFrameUserPath(metadata.FZFVersion)
	metadata.GoVersion = redactFirstFrameUserPath(metadata.GoVersion)
	metadata.Antivirus = redactFirstFrameUserPath(metadata.Antivirus)
	metadata.Power = redactFirstFrameUserPath(metadata.Power)
	return metadata
}

func redactFirstFrameUserPath(value string) string {
	lower := strings.ToLower(value)
	absolute := filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.Contains(lower, `\users\`) || strings.Contains(lower, "/home/") || strings.Contains(lower, `\appdata\`)
	if !absolute {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + fmt.Sprintf("%x", digest)
}

func firstFrameJSONMetrics(report firstFrameComparisonReport) map[string]firstFrameJSONMetric {
	result := make(map[string]firstFrameJSONMetric, len(report.metrics))
	for name, metric := range report.metrics {
		value := firstFrameJSONMetric{Unit: firstFrameMetricUnit(name)}
		if metric.enabledPresent {
			value.Enabled = firstFrameJSONDistributionFor(metric.enabled)
		}
		if metric.disabledPresent {
			value.Disabled = firstFrameJSONDistributionFor(metric.disabled)
		}
		if metric.delta != nil {
			value.Delta = &firstFrameJSONDelta{P50: metric.delta.P50US, P95: metric.delta.P95US,
				P50CI: metric.delta.P50CI, P95CI: metric.delta.P95CI}
		}
		result[name] = value
	}
	return result
}

func firstFrameJSONDistributionFor(distribution firstFrameDistribution) *firstFrameJSONDistribution {
	return &firstFrameJSONDistribution{P50: distribution.P50US, P95: distribution.P95US,
		P50CI: distribution.P50CI, P95CI: distribution.P95CI}
}

func firstFrameMetricUnit(name string) string {
	if strings.HasSuffix(name, "_duration") || name == "first_byte" || name == "meaningful_frame" ||
		name == "fzf_start" || name == "zoxide_terminal" || name == "preview_dispatch" || name == "preview_complete" {
		return "us"
	}
	return "count"
}

func enforceFirstFrameAcceptance(t *testing.T, pairs []firstFramePair, comparison firstFrameComparisonReport) {
	t.Helper()
	if err := firstFrameAcceptanceError(pairs, comparison); err != nil {
		t.Fatal(err)
	}
}

func firstFrameAcceptanceError(pairs []firstFramePair, comparison firstFrameComparisonReport) error {
	meaningful := comparison.metrics["meaningful_frame"]
	if !meaningful.enabledPresent || meaningful.enabled.P95US >= 170_000 {
		return fmt.Errorf("enabled meaningful-current-frame p95=%dus; acceptance requires <170000us", meaningful.enabled.P95US)
	}
	firstByte := comparison.metrics["first_byte"]
	if firstByte.delta == nil || firstByte.delta.P95US > 10_000 {
		return fmt.Errorf("first-byte p95 delta=%+v; acceptance permits <=10000us regression", firstByte.delta)
	}
	callbacks := comparison.metrics["callback_through_frame"]
	if callbacks.delta == nil || callbacks.enabled.P50US >= callbacks.disabled.P50US || callbacks.enabled.P50US*4 > callbacks.disabled.P50US*3 {
		return fmt.Errorf("callback starts are not materially reduced: enabled=%+v disabled=%+v delta=%+v", callbacks.enabled, callbacks.disabled, callbacks.delta)
	}
	expectedCandidates := 0
	for index, pair := range pairs {
		for _, sample := range []firstFrameSample{pair.disabled, pair.enabled} {
			if expectedCandidates == 0 {
				expectedCandidates = sample.candidateCount
			}
			if sample.candidateCount != expectedCandidates || sample.candidateCount <= 0 || !sample.metrics.previewCompletePresent || sample.processPreviewComplete.renderers["eza"] == 0 {
				return fmt.Errorf("pair %d %s has lifecycle/rendering regression: candidate=%d metrics=%+v processes=%+v", index, sample.mode, sample.candidateCount, sample.metrics, sample.processPreviewComplete)
			}
			data, err := json.Marshal(sample.events)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{"FZF_API_KEY=", "SHELL_PICKER_ADDR=", "SHELL_PICKER_TOKEN=", "query-canary", "state-canary"} {
				if strings.Contains(string(data), forbidden) {
					return fmt.Errorf("pair %d %s trace leaked %q", index, sample.mode, forbidden)
				}
			}
		}
	}
	return nil
}
