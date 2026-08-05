package integration

import (
	"strings"
	"testing"
)

func TestPerformanceDocumentationStatesExperimentalFZFSidecarContract(t *testing.T) {
	document := readDoc(t, "docs/performance.md")
	for _, required := range []string{
		"SHELL_PICKER_EXPERIMENTAL_FZF_SIDECAR=1",
		"disabled by default",
		"list-label",
		"static initial `--header`",
		"startup and resize display callbacks",
		"omits both startup and resize `transform(d)` display callbacks",
		"fzf's native header truncation",
		"inherited `FZF_API_KEY` remains",
		"meaningful p95 <170ms",
		"first-byte regression <=10ms",
		"materially reduced callback count",
		">=30 samples",
		"residual port time-of-check/time-of-use race",
		"prototype criteria",
		"rollback",
	} {
		if !strings.Contains(document, required) {
			t.Errorf("performance documentation missing experimental sidecar contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"enabled by default",
		"stable default",
		"production default",
	} {
		if strings.Contains(strings.ToLower(document), forbidden) {
			t.Errorf("performance documentation presents experimental sidecar as %q", forbidden)
		}
	}
}

func TestPerformanceDocumentationStatesProvisionalFirstFrameQualification(t *testing.T) {
	document := readDoc(t, "docs/performance.md")
	normalized := strings.Join(strings.Fields(strings.ToLower(document)), " ")
	for _, required := range []string{
		"runtime remains opt-in and default-off",
		"formal status remains `baseline-required`/`diagnostic-unqualified`",
		"qualified rerun pending",
		"two 30-pair diagnostic runs",
		"enabled meaningful-frame p95 was 145 ms in the first run and 220 ms in the second",
		"callback median reduced from 3 to 0 in the first run and from 3 to 1 in the second",
		"preview-complete median improved by roughly 90-100 ms",
		"first-byte p95 delta was +7 ms in the first run and +26.8 ms in the second",
		"user accepted a provisional merge",
		"second run missing both the <170 ms meaningful-frame p95 and <=+10 ms first-byte criteria",
		"No default rollout claim is made",
	} {
		phrase := strings.Join(strings.Fields(strings.ToLower(required)), " ")
		if !strings.Contains(normalized, phrase) {
			t.Errorf("performance documentation missing provisional first-frame qualification statement %q", required)
		}
	}
}

func TestDocumentationStatesReadinessTimeoutRetryContract(t *testing.T) {
	documentation := readDoc(t, "docs/performance.md")
	for _, required := range []string{
		"readiness interval/deadline",
		"request timeout/deadline failures retry",
		"while the parent/session context remains live",
		"parent cancellation/deadline stops immediately",
	} {
		if !strings.Contains(documentation, required) {
			t.Errorf("documentation missing readiness timeout contract %q", required)
		}
	}
}
