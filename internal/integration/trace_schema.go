package integration

import (
	"math"
	"time"
)

type traceSchemaRules struct {
	EventOutcomes        map[string][]string
	RendererBases        []string
	ZoxidePolicies       []string
	ZoxideOutcomes       []string
	GenerationMax        uint64
	CandidateCountMin    int
	CandidateCountMax    int
	ChildStartsMin       int
	ChildStartsMax       int
	MaxLiveChildrenMin   int
	MaxLiveChildrenMax   int
	ZoxideCounterMin     int
	ZoxideAttemptsMax    int
	ZoxideLiveRequired   int
	DurationMin          time.Duration
	DurationMax          time.Duration
	TimestampPastLimit   time.Duration
	TimestampFutureLimit time.Duration
	TimestampRequired    bool
	TimestampFormat      string
	TimestampMustBeUTC   bool
}

const traceTimestampRFC3339Nano = "rfc3339nano"

var productionTraceSchema = traceSchemaRules{
	EventOutcomes: map[string][]string{
		"session.start":      {"cd", "cp"},
		"generation.start":   {"ok"},
		"generation.publish": {"ok"},
		"generation.discard": {"cancelled", "error", "stale", "superseded"},
		"fzf.start":          {"ok"},
		"fzf.exit":           {"ok", "aborted", "error"},
		"callback.event":     {"mi", "ma", "es", "fw", "up", "sl", "hm", "en"},
		"callback.load":      {"ok", "error"},
		"preview.dispatch":   {"ok", "error"},
		"preview.finished":   {"ok", "error"},
		"preview.cancel":     {"cancelled"},
		"preview.exit":       {"ok", "error"},
		"session.close":      {"accepted", "aborted", "error"},
	},
	RendererBases:        []string{"native", "eza", "glow", "bat", "kitten", "chafa", "unzip", "gzip", "xz", "tar", "file", "pdftoppm", "ffmpegthumbnailer", "ffmpeg", "exiftool"},
	ZoxidePolicies:       []string{"cached", "fresh"},
	ZoxideOutcomes:       []string{"ok", "missing", "process-error", "malformed", "timeout", "cancelled", "not-run", "cached"},
	GenerationMax:        math.MaxUint64,
	CandidateCountMin:    0,
	CandidateCountMax:    1_000_000,
	ChildStartsMin:       0,
	ChildStartsMax:       3,
	MaxLiveChildrenMin:   0,
	MaxLiveChildrenMax:   1,
	ZoxideCounterMin:     0,
	ZoxideAttemptsMax:    1_000_000,
	ZoxideLiveRequired:   0,
	DurationMin:          0,
	DurationMax:          24 * time.Hour,
	TimestampPastLimit:   24 * time.Hour,
	TimestampFutureLimit: time.Second,
	TimestampRequired:    true,
	TimestampFormat:      traceTimestampRFC3339Nano,
	TimestampMustBeUTC:   true,
}

func traceSchemaAuthority() traceSchemaRules {
	return productionTraceSchema
}

func traceTimestampLayout(format string) string {
	if format == traceTimestampRFC3339Nano {
		return time.RFC3339Nano
	}
	return format
}

func schemaContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
