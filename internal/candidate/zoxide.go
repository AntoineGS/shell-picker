package candidate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

const zoxideWaitDelay = 250 * time.Millisecond

var (
	errZoxideNotReady = errors.New("zoxide cache is not ready")
	errZoxideTimeout  = fmt.Errorf("zoxide private timeout: %w", context.DeadlineExceeded)
)

type zoxideWaiterCancellationError struct {
	cause error
}

func (err *zoxideWaiterCancellationError) Error() string {
	return fmt.Sprintf("zoxide cache waiter canceled: %v", err.cause)
}

func (err *zoxideWaiterCancellationError) Unwrap() error {
	return err.cause
}

type ZoxidePolicy uint8

const (
	ZoxideCached ZoxidePolicy = iota + 1
	ZoxideFresh
)

func (policy ZoxidePolicy) String() string {
	switch policy {
	case ZoxideCached:
		return "cached"
	case ZoxideFresh:
		return "fresh"
	default:
		return fmt.Sprintf("ZoxidePolicy(%d)", policy)
	}
}

func ParseZoxidePolicy(value string) (ZoxidePolicy, error) {
	switch value {
	case "cached":
		return ZoxideCached, nil
	case "fresh":
		return ZoxideFresh, nil
	default:
		return 0, fmt.Errorf("invalid zoxide policy %q", value)
	}
}

type SourceMetrics struct {
	LocalDuration   time.Duration
	ZoxideDuration  time.Duration
	ZoxideOutcome   string
	ZoxideAttempts  int
	ZoxideStarts    int
	ZoxideExits     int
	ZoxideProcesses int
	ZoxideLive      int
	ZoxideMaxLive   int
}

type ZoxideCache struct {
	once        sync.Once
	ready       chan struct{}
	runner      process.Runner
	path        string
	environment []string
	timeout     time.Duration
	records     []Record
	metrics     SourceMetrics
	err         error
}

func NewZoxideCache(runner process.Runner, path string, environment []string, timeout time.Duration) (*ZoxideCache, error) {
	if timeout < 0 {
		return nil, errors.New("zoxide timeout must not be negative")
	}
	return &ZoxideCache{
		ready:       make(chan struct{}),
		runner:      runner,
		path:        path,
		environment: append([]string(nil), environment...),
		timeout:     timeout,
	}, nil
}

func DefaultZoxideTimeout() time.Duration {
	if runtime.GOOS == "windows" {
		return 150 * time.Millisecond
	}
	return 75 * time.Millisecond
}

// Load starts one shared load. The caller that starts it waits for the terminal
// result; later callers can stop waiting without interrupting that load.
func (cache *ZoxideCache) Load(ctx context.Context) error {
	started := false
	cache.once.Do(func() {
		started = true
		go func() {
			defer close(cache.ready)
			cache.load(ctx)
		}()
	})
	if started || ctx == nil {
		<-cache.ready
		return cache.err
	}
	select {
	case <-cache.ready:
		return cache.err
	case <-ctx.Done():
		cause := context.Cause(ctx)
		if cause == nil {
			cause = ctx.Err()
		}
		return &zoxideWaiterCancellationError{cause: cause}
	}
}

func (cache *ZoxideCache) Records() ([]Record, SourceMetrics, error) {
	select {
	case <-cache.ready:
		return cloneRecords(cache.records), cache.metrics, cache.err
	default:
		return nil, SourceMetrics{}, errZoxideNotReady
	}
}

func (cache *ZoxideCache) load(ctx context.Context) {
	started := time.Now()
	cache.metrics.ZoxideOutcome = "process-error"
	if ctx == nil {
		cache.err = errors.New("zoxide: nil context")
		cache.metrics.ZoxideDuration = time.Since(started)
		return
	}

	runCtx := ctx
	timeoutCancel := func() {}
	if cache.timeout > 0 {
		runCtx, timeoutCancel = context.WithTimeoutCause(ctx, cache.timeout, errZoxideTimeout)
	}
	defer timeoutCancel()
	runCtx, outputCancel := context.WithCancelCause(runCtx)
	defer outputCancel(nil)

	tracker := newZoxideTracker(cache.runner.Observe)
	runner := cache.runner
	runner.Observe = tracker.observe
	var stdout bytes.Buffer
	limitedStdout := newZoxideLimitWriter(&stdout, MaxZoxideOutputBytes, MaxZoxideRows, MaxZoxideRowBytes, outputCancel)
	runErr := runner.Run(runCtx, process.Spec{
		Path:        cache.path,
		Args:        []string{"query", "--list"},
		Env:         process.SanitizeEnv(cache.environment, nil),
		Stdout:      limitedStdout,
		Containment: process.ContainmentOwnTree,
		WaitDelay:   zoxideWaitDelay,
	})
	cache.metrics.ZoxideDuration = time.Since(started)
	cache.metrics.ZoxideAttempts, cache.metrics.ZoxideStarts, cache.metrics.ZoxideExits,
		cache.metrics.ZoxideProcesses, cache.metrics.ZoxideLive, cache.metrics.ZoxideMaxLive = tracker.metrics()
	outputErr := limitedStdout.finalize()

	switch {
	case errors.Is(outputErr, errZoxideOutputLimit):
		cache.metrics.ZoxideOutcome = "malformed"
		cache.err = errZoxideOutputLimit
	case errors.Is(runErr, errZoxideTimeout):
		cache.metrics.ZoxideOutcome = "timeout"
		cache.err = runErr
	case ctx.Err() != nil:
		cache.metrics.ZoxideOutcome = "cancelled"
		cache.err = context.Cause(ctx)
	case errors.Is(runErr, exec.ErrNotFound):
		cache.metrics.ZoxideOutcome = "missing"
		cache.err = runErr
	case runErr != nil:
		cache.metrics.ZoxideOutcome = "process-error"
		cache.err = runErr
	default:
		records, err := parseZoxideRecords(stdout.Bytes())
		if err != nil {
			cache.metrics.ZoxideOutcome = "malformed"
			cache.err = err
			return
		}
		cache.records = records
		cache.metrics.ZoxideOutcome = "ok"
	}
}

func parseZoxideRecords(output []byte) ([]Record, error) {
	if err := validateZoxideOutputBounds(output); err != nil {
		return nil, err
	}
	if len(output) == 0 {
		return []Record{}, nil
	}
	if output[len(output)-1] == '\n' {
		output = output[:len(output)-1]
	}
	if len(output) == 0 {
		return nil, errors.New("zoxide output contains an empty row")
	}
	records := make([]Record, 0)
	for start := 0; start < len(output); {
		end := bytes.IndexByte(output[start:], '\n')
		if end < 0 {
			end = len(output) - start
		}
		row := output[start : start+end]
		if len(row) == 0 {
			return nil, errors.New("zoxide output contains an empty row")
		}
		if bytes.IndexByte(row, 0) >= 0 {
			return nil, errors.New("zoxide output contains NUL")
		}
		if !filepath.IsAbs(string(row)) {
			return nil, fmt.Errorf("zoxide path %q is not absolute", row)
		}
		records = append(records, newRecord(protocol.KindZoxide, protocol.EscapeDisplay(row), row))
		start += end
		if start < len(output) && output[start] == '\n' {
			start++
		}
	}
	return records, nil
}

func validateZoxideOutputBounds(output []byte) error {
	writer := newZoxideLimitWriter(io.Discard, MaxZoxideOutputBytes, MaxZoxideRows, MaxZoxideRowBytes, nil)
	if _, err := writer.Write(output); err != nil {
		return err
	}
	return writer.finalize()
}

type zoxideTracker struct {
	mu         sync.Mutex
	downstream func(process.ProcessEvent)
	attempts   int
	starts     int
	exits      int
	processes  int
	live       int
	maxLive    int
}

func newZoxideTracker(observer func(process.ProcessEvent)) *zoxideTracker {
	return &zoxideTracker{downstream: observer}
}

func (tracker *zoxideTracker) observe(event process.ProcessEvent) {
	tracker.mu.Lock()
	switch event.Phase {
	case "attempt":
		tracker.attempts++
	case "start":
		tracker.starts++
		tracker.processes++
		tracker.live++
		if tracker.live > tracker.maxLive {
			tracker.maxLive = tracker.live
		}
	case "exit":
		tracker.exits++
		tracker.live--
	}
	tracker.mu.Unlock()
	if tracker.downstream != nil {
		tracker.downstream(event)
	}
}

func (tracker *zoxideTracker) metrics() (int, int, int, int, int, int) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.attempts, tracker.starts, tracker.exits, tracker.processes, tracker.live, tracker.maxLive
}

func cloneRecords(records []Record) []Record {
	cloned := make([]Record, len(records))
	for index, record := range records {
		cloned[index] = record
		cloned[index].Path = bytes.Clone(record.Path)
		cloned[index].Target = pathutil.Location{Kind: record.Target.Kind, Path: bytes.Clone(record.Target.Path)}
	}
	return cloned
}
