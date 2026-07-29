package app

import (
	"errors"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/session"
	"github.com/AntoineGS/shell-picker/internal/sessionipc"
)

const (
	maxMetricCount  = 1_000_000
	maxMetricLabels = 64
)

type pickerMetrics struct {
	mu      sync.Mutex
	traceID [16]byte

	events, loads, previews  uint64
	callbackIPC, loadLatency time.Duration
	queueWait, transform     time.Duration
	sources                  candidate.SourceMetrics

	previewStarted, previewFinished uint64
	previewDuration                 time.Duration
	previewChildStarts              uint64
	previewMaxLive                  int
	rendererStarted                 map[string]uint64
	rendererFinished                map[string]uint64
	previewOutcomes                 map[string]uint64
	rendererOverflow                uint64
	outcomeOverflow                 uint64
}

func (metrics *pickerMetrics) recordTransition(result session.TransitionResult) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.events = boundedIncrement(metrics.events)
	metrics.queueWait = saturatingDuration(metrics.queueWait, result.Metrics.QueueWait)
	metrics.transform = saturatingDuration(metrics.transform, result.Metrics.TransformDuration)
	metrics.sources.LocalDuration = saturatingDuration(metrics.sources.LocalDuration, result.Metrics.Sources.LocalDuration)
	metrics.sources.ZoxideDuration = saturatingDuration(metrics.sources.ZoxideDuration, result.Metrics.Sources.ZoxideDuration)
	metrics.sources.ZoxideOutcome = result.Metrics.Sources.ZoxideOutcome
	metrics.sources.ZoxideAttempts = boundedIntAdd(metrics.sources.ZoxideAttempts, result.Metrics.Sources.ZoxideAttempts)
	metrics.sources.ZoxideStarts = boundedIntAdd(metrics.sources.ZoxideStarts, result.Metrics.Sources.ZoxideStarts)
	if result.Metrics.Sources.ZoxideMaxLive > metrics.sources.ZoxideMaxLive {
		metrics.sources.ZoxideMaxLive = result.Metrics.Sources.ZoxideMaxLive
	}
}

func (metrics *pickerMetrics) recordCallback(duration time.Duration) {
	metrics.mu.Lock()
	metrics.callbackIPC = saturatingDuration(metrics.callbackIPC, duration)
	metrics.mu.Unlock()
}

func (metrics *pickerMetrics) recordLoad(duration time.Duration) {
	metrics.mu.Lock()
	metrics.loads = boundedIncrement(metrics.loads)
	metrics.loadLatency = saturatingDuration(metrics.loadLatency, duration)
	metrics.mu.Unlock()
}

func (metrics *pickerMetrics) recordPreviewResolve(duration time.Duration) {
	metrics.mu.Lock()
	metrics.previews = boundedIncrement(metrics.previews)
	metrics.callbackIPC = saturatingDuration(metrics.callbackIPC, duration)
	metrics.mu.Unlock()
}

func (metrics *pickerMetrics) recordPreview(request sessionipc.PreviewRequest) error {
	if err := validatePreviewMetric(request); err != nil {
		return err
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.previews = boundedIncrement(metrics.previews)
	switch request.Phase {
	case "started":
		metrics.previewStarted = boundedIncrement(metrics.previewStarted)
		metrics.incrementLabel(&metrics.rendererStarted, request.Renderer, &metrics.rendererOverflow)
	case "finished":
		metrics.previewFinished = boundedIncrement(metrics.previewFinished)
		metrics.previewDuration = saturatingDuration(metrics.previewDuration, time.Duration(request.DurationUS)*time.Microsecond)
		metrics.previewChildStarts = boundedAdd(metrics.previewChildStarts, uint64(request.ChildStarts))
		if request.MaxLiveChildren > metrics.previewMaxLive {
			metrics.previewMaxLive = request.MaxLiveChildren
		}
		metrics.incrementLabel(&metrics.rendererFinished, request.Renderer, &metrics.rendererOverflow)
		metrics.incrementLabel(&metrics.previewOutcomes, request.Outcome, &metrics.outcomeOverflow)
	}
	return nil
}

func (metrics *pickerMetrics) incrementLabel(values *map[string]uint64, label string, overflow *uint64) {
	if *values == nil {
		*values = make(map[string]uint64)
	}
	if current, exists := (*values)[label]; exists {
		(*values)[label] = boundedIncrement(current)
	} else if len(*values) < maxMetricLabels {
		(*values)[label] = 1
	} else {
		*overflow = boundedIncrement(*overflow)
	}
}

func validatePreviewMetric(request sessionipc.PreviewRequest) error {
	switch request.Phase {
	case "started":
		if !validMetricLabel(request.Renderer) || request.DurationUS != 0 || request.ChildStarts != 0 ||
			request.MaxLiveChildren != 0 || request.Outcome != "" {
			return errors.New("invalid started preview metric")
		}
	case "finished":
		if !validMetricLabel(request.Renderer) || !validMetricLabel(request.Outcome) || request.DurationUS < 0 ||
			request.DurationUS > int64(10*time.Second/time.Microsecond) || request.ChildStarts < 0 || request.ChildStarts > 3 ||
			request.MaxLiveChildren < 0 || request.MaxLiveChildren > 1 || request.MaxLiveChildren > request.ChildStarts ||
			(request.Renderer == "native" && (request.ChildStarts != 0 || request.MaxLiveChildren != 0)) {
			return errors.New("invalid finished preview metric")
		}
	default:
		return errors.New("invalid preview metric phase")
	}
	return nil
}

func validMetricLabel(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func boundedIncrement(value uint64) uint64 { return boundedAdd(value, 1) }

func boundedAdd(value, delta uint64) uint64 {
	if value >= maxMetricCount || delta > maxMetricCount-value {
		return maxMetricCount
	}
	return value + delta
}

func boundedIntAdd(value, delta int) int {
	if value >= maxMetricCount || delta > maxMetricCount-value {
		return maxMetricCount
	}
	return value + delta
}

func saturatingDuration(value, delta time.Duration) time.Duration {
	if delta > 0 && value > time.Duration(1<<63-1)-delta {
		return time.Duration(1<<63 - 1)
	}
	return value + delta
}
