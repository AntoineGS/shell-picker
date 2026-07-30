package integration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AntoineGS/shell-picker/internal/candidate"
	"github.com/AntoineGS/shell-picker/internal/fzf"
	"github.com/AntoineGS/shell-picker/internal/pathutil"
	"github.com/AntoineGS/shell-picker/internal/process"
	"github.com/AntoineGS/shell-picker/internal/protocol"
)

var (
	stableStringSink  string
	stableBytesSink   []byte
	stableRecordsSink []candidate.Record
)

func TestStablePerformanceGates(t *testing.T) {
	t.Run("pure allocations", testPureAllocationGates)
	t.Run("cached cd", func(t *testing.T) { testCandidateProcessBudget(t, "cached", protocol.PickerCD, 2, "ok") })
	t.Run("fresh cd", func(t *testing.T) { testCandidateProcessBudget(t, "fresh", protocol.PickerCD, 2, "ok") })
	t.Run("cached cp", func(t *testing.T) { testCandidateProcessBudget(t, "cached", protocol.PickerCP, 2, "not-run") })
	t.Run("fresh cp", func(t *testing.T) { testCandidateProcessBudget(t, "fresh", protocol.PickerCP, 2, "not-run") })
	t.Run("missing zoxide", func(t *testing.T) { testFailedZoxideBudget(t, filepath.Join(t.TempDir(), "missing"), "missing") })
	t.Run("spawn failure", func(t *testing.T) { testFailedZoxideBudget(t, t.TempDir(), "process-error") })
	t.Run("fresh cancellation and independent sessions", testFreshConcurrencyPerformanceGates)
	t.Run("owned cancellation resources", func(t *testing.T) { runTask20ResourceIterations(t, 1) })
}

func testFreshConcurrencyPerformanceGates(t *testing.T) {
	root := t.TempDir()
	environment := append(os.Environ(), parityHelperEnvironment+"=performance", "GO_PERF_HELPER=zoxide",
		"GO_PERF_ZOXIDE_PATH="+root, "GORACE=atexit_sleep_ms=0")
	started, release := make(chan struct{}, 2), make(chan struct{}, 2)
	counts := new(performanceProcessCounts)
	runner := process.Runner{Observe: func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
			<-release
		}
	}}
	newCache := func() (*candidate.ZoxideCache, error) {
		return candidate.NewZoxideCache(runner, os.Args[0], environment, 0)
	}
	request := candidate.BuildRequest{Picker: protocol.PickerCD, Location: pathutil.Filesystem([]byte(root)), Initial: true, StatWorkers: 2}

	var factoryCalls atomic.Int32
	serialized := new(candidate.Builder)
	serialized.ConfigureFresh(func() (*candidate.ZoxideCache, error) {
		factoryCalls.Add(1)
		return newCache()
	})
	firstDone := make(chan error, 1)
	go func() {
		_, err := serialized.Build(context.Background(), request)
		firstDone <- err
	}()
	awaitPerformanceSignal(t, started, "first fresh process start")
	cause := errors.New("cancelled fresh waiter")
	waiterCtx, cancelWaiter := context.WithCancelCause(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := serialized.Build(waiterCtx, request)
		waiterDone <- err
	}()
	cancelWaiter(cause)
	if err := awaitPerformanceError(t, waiterDone, "cancelled fresh waiter"); !errors.Is(err, cause) {
		t.Fatalf("waiter error=%v", err)
	}
	if factoryCalls.Load() != 1 {
		t.Fatalf("cancelled waiter factory calls=%d", factoryCalls.Load())
	}
	release <- struct{}{}
	if err := awaitPerformanceError(t, firstDone, "first fresh completion"); err != nil {
		t.Fatal(err)
	}
	counts.assert(t, 1, 1, 1, 1)

	counts = new(performanceProcessCounts)
	runner.Observe = func(event process.ProcessEvent) {
		counts.observe(event)
		if event.Phase == "start" {
			started <- struct{}{}
			<-release
		}
	}
	builders := [2]*candidate.Builder{new(candidate.Builder), new(candidate.Builder)}
	done := make(chan error, 2)
	for _, builder := range builders {
		builder.ConfigureFresh(func() (*candidate.ZoxideCache, error) {
			return candidate.NewZoxideCache(runner, os.Args[0], environment, 0)
		})
		go func(builder *candidate.Builder) {
			_, err := builder.Build(context.Background(), request)
			done <- err
		}(builder)
	}
	awaitPerformanceSignal(t, started, "independent fresh start one")
	awaitPerformanceSignal(t, started, "independent fresh start two")
	release <- struct{}{}
	release <- struct{}{}
	for range builders {
		if err := awaitPerformanceError(t, done, "independent fresh completion"); err != nil {
			t.Fatal(err)
		}
	}
	counts.assert(t, 2, 2, 2, 2)
}

func awaitPerformanceSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timeout waiting for %s", name)
	}
}

func awaitPerformanceError(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatalf("timeout waiting for %s", name)
		return ctx.Err()
	}
}

func TestPerformanceMakeTargetsUseCompiledHarnessAndFiftySamples(t *testing.T) {
	command := exec.Command("make", "-n", "performance-stable", "performance-dedicated")
	command.Dir = ".."
	command.Env = append(os.Environ(), "SHELL_PICKER_DEDICATED_PERF=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("make dry run: %v\n%s", err, output)
	}
	text := string(output)
	for _, required := range []string{"TestStablePerformanceGates", "go build -trimpath", "go test -c", "-samples 50",
		"TestDedicatedBaseline", "TestDedicatedTargets"} {
		if !strings.Contains(text, required) {
			t.Fatalf("make output lacks %q:\n%s", required, text)
		}
	}
	if strings.Contains(text, "go run") {
		t.Fatalf("dedicated harness uses go run:\n%s", text)
	}
	for _, path := range []string{"host-baseline.json", "performance.json"} {
		ignored := exec.Command("git", "check-ignore", "--quiet", path)
		ignored.Dir = ".."
		if output, err := ignored.CombinedOutput(); err != nil {
			t.Fatalf("dedicated output %s is not ignored: %v %s", path, err, output)
		}
	}
}

func testPureAllocationGates(t *testing.T) {
	t.Helper()
	path := []byte("directory with spaces/and\\controls\t")
	wire := protocol.WireRecord{Kind: protocol.KindDirectory, Display: "directory", Payload: "L3RtcA=="}
	effect := protocol.Effect{Search: "on", Rebind: protocol.ModeNormal, ClearMulti: true, ReloadGeneration: 2,
		ClearQuery: true, Prompt: "[N] /tmp "}
	local := []candidate.Record{performanceRecord(protocol.KindLocal, "/local"), performanceRecord(protocol.KindDirectory, "/same")}
	zoxide := []candidate.Record{performanceRecord(protocol.KindZoxide, "/same"), performanceRecord(protocol.KindZoxide, "/zoxide")}

	allocationGate(t, "padded codec", 2, func() { stableStringSink = protocol.EncodePath(path) })
	allocationGate(t, "display", 1, func() { stableStringSink = protocol.EscapeDisplay(path) })
	allocationGate(t, "record", 1, func() { stableBytesSink = wire.Bytes() })
	allocationGate(t, "action", 8, func() {
		value, err := fzf.RenderEffect(effect)
		if err != nil {
			t.Fatal(err)
		}
		stableStringSink = value
	})
	allocationGate(t, "merge", 6, func() { stableRecordsSink = candidate.MergeRecords(local, zoxide) })
}

func allocationGate(t *testing.T, name string, maximum float64, operation func()) {
	t.Helper()
	if allocations := testing.AllocsPerRun(1000, operation); allocations > maximum {
		t.Errorf("%s allocations=%g limit=%g", name, allocations, maximum)
	}
}

func performanceRecord(kind protocol.Kind, path string) candidate.Record {
	raw := []byte(path)
	return candidate.Record{Kind: kind, Display: path, Path: raw, Payload: protocol.EncodePath(raw), Target: pathutil.Filesystem(raw)}
}

type performanceProcessCounts struct {
	mu                                     sync.Mutex
	attempts, starts, exits, live, maxLive int
}

func (counts *performanceProcessCounts) observe(event process.ProcessEvent) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	switch event.Phase {
	case "attempt":
		counts.attempts++
	case "start":
		counts.starts++
		counts.live++
		counts.maxLive = max(counts.maxLive, counts.live)
	case "exit":
		counts.exits++
		counts.live--
	}
}

func (counts *performanceProcessCounts) assert(t *testing.T, attempts, starts, exits, maxLive int) {
	t.Helper()
	counts.mu.Lock()
	defer counts.mu.Unlock()
	if counts.attempts != attempts || counts.starts != starts || counts.exits != exits || counts.maxLive != maxLive ||
		counts.live != 0 || counts.starts != counts.exits {
		t.Fatalf("process counters attempts=%d starts=%d exits=%d live=%d max=%d; want %d/%d/%d/0/%d",
			counts.attempts, counts.starts, counts.exits, counts.live, counts.maxLive, attempts, starts, exits, maxLive)
	}
}

func testCandidateProcessBudget(t *testing.T, policy string, picker protocol.Picker, generations int, wantedOutcome string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	counts := new(performanceProcessCounts)
	runner := process.Runner{Observe: counts.observe}
	environment := append(os.Environ(), parityHelperEnvironment+"=performance", "GO_PERF_HELPER=zoxide",
		"GO_PERF_ZOXIDE_PATH="+root, "GORACE=atexit_sleep_ms=0")
	newCache := func() (*candidate.ZoxideCache, error) {
		return candidate.NewZoxideCache(runner, os.Args[0], environment, time.Second)
	}
	builder := new(candidate.Builder)
	if policy == "cached" {
		cache, err := newCache()
		if err != nil {
			t.Fatal(err)
		}
		builder.ConfigureCached(cache)
	} else {
		builder.ConfigureFresh(newCache)
	}
	for generation := 0; generation < generations; generation++ {
		result, err := builder.Build(context.Background(), candidate.BuildRequest{Picker: picker,
			Location: pathutil.Filesystem([]byte(root)), Initial: generation == 0, StatWorkers: 2})
		if err != nil {
			t.Fatal(err)
		}
		outcome := result.Metrics.ZoxideOutcome
		if picker == protocol.PickerCD && policy == "cached" && generation > 0 {
			outcome = "ok"
			if result.Metrics.ZoxideOutcome != "cached" || result.Metrics.ZoxideAttempts != 0 || result.Metrics.ZoxideStarts != 0 {
				t.Fatalf("cached generation %d metrics=%+v", generation+1, result.Metrics)
			}
		}
		if outcome != wantedOutcome {
			t.Fatalf("generation %d outcome=%q metrics=%+v", generation+1, outcome, result.Metrics)
		}
		if picker == protocol.PickerCD && (policy == "fresh" || generation == 0) &&
			(result.Metrics.ZoxideAttempts != 1 || result.Metrics.ZoxideStarts != 1 || result.Metrics.ZoxideMaxLive != 1) {
			t.Fatalf("generation %d metrics=%+v", generation+1, result.Metrics)
		}
		if picker == protocol.PickerCP && (result.Metrics.ZoxideAttempts != 0 || result.Metrics.ZoxideStarts != 0 || result.Metrics.ZoxideMaxLive != 0) {
			t.Fatalf("cp generation %d metrics=%+v", generation+1, result.Metrics)
		}
	}
	processes := 0
	if picker == protocol.PickerCD {
		processes = generations
		if policy == "cached" {
			processes = 1
		}
	}
	counts.assert(t, processes, processes, processes, min(processes, 1))
}

func testFailedZoxideBudget(t *testing.T, path, outcome string) {
	t.Helper()
	root := t.TempDir()
	counts := new(performanceProcessCounts)
	cache, err := candidate.NewZoxideCache(process.Runner{Observe: counts.observe}, path, os.Environ(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	builder := new(candidate.Builder)
	builder.ConfigureCached(cache)
	result, err := builder.Build(context.Background(), candidate.BuildRequest{Picker: protocol.PickerCD,
		Location: pathutil.Filesystem([]byte(root)), Initial: true, StatWorkers: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Metrics.ZoxideOutcome != outcome || result.Metrics.ZoxideAttempts != 1 || result.Metrics.ZoxideStarts != 0 ||
		result.Metrics.ZoxideMaxLive != 0 {
		t.Fatalf("metrics=%+v", result.Metrics)
	}
	counts.assert(t, 1, 0, 0, 0)
}

func BenchmarkCandidatePerformanceScenarios(b *testing.B) {
	scenarios := []struct {
		name, policy, mode string
		picker             protocol.Picker
		generations        int
		timeout            time.Duration
		attempts, starts   int
	}{
		{name: "initial-cached-overlap-10000", policy: "cached", mode: "records-10000", picker: protocol.PickerCD,
			generations: 1, timeout: time.Second, attempts: 1, starts: 1},
		{name: "cached-repeated", policy: "cached", mode: "present", picker: protocol.PickerCD,
			generations: 3, timeout: time.Second, attempts: 1, starts: 1},
		{name: "fresh-repeated", policy: "fresh", mode: "present", picker: protocol.PickerCD,
			generations: 3, timeout: time.Second, attempts: 3, starts: 3},
		{name: "timeout-discard", policy: "cached", mode: "timeout", picker: protocol.PickerCD,
			generations: 1, timeout: candidate.DefaultZoxideTimeout(), attempts: 1, starts: 1},
		{name: "missing", policy: "cached", mode: "missing", picker: protocol.PickerCD,
			generations: 1, timeout: time.Second, attempts: 1, starts: 0},
		{name: "spawn-failure", policy: "cached", mode: "spawn-failure", picker: protocol.PickerCD,
			generations: 1, timeout: time.Second, attempts: 1, starts: 0},
		{name: "cp-cached", policy: "cached", mode: "present", picker: protocol.PickerCP,
			generations: 3, timeout: time.Second},
		{name: "cp-fresh", policy: "fresh", mode: "present", picker: protocol.PickerCP,
			generations: 3, timeout: time.Second},
	}
	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			benchmarkCandidateScenario(b, scenario.policy, scenario.mode, scenario.picker, scenario.generations,
				scenario.timeout, scenario.attempts, scenario.starts)
		})
	}
}

func benchmarkCandidateScenario(b *testing.B, policy, mode string, picker protocol.Picker, generations int,
	timeout time.Duration, attempts, starts int) {
	b.Helper()
	root := b.TempDir()
	counts := new(performanceProcessCounts)
	environment := append(os.Environ(), parityHelperEnvironment+"=performance", "GO_PERF_HELPER=zoxide",
		"GO_PERF_ZOXIDE_PATH="+root, "GO_PERF_ZOXIDE_MODE="+mode, "GORACE=atexit_sleep_ms=0")
	path := os.Args[0]
	if mode == "missing" {
		path = filepath.Join(root, "missing")
	} else if mode == "spawn-failure" {
		path = root
	}
	runner := process.Runner{Observe: counts.observe}
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		newCache := func() (*candidate.ZoxideCache, error) {
			return candidate.NewZoxideCache(runner, path, environment, timeout)
		}
		builder := new(candidate.Builder)
		if policy == "cached" {
			cache, err := newCache()
			if err != nil {
				b.Fatal(err)
			}
			builder.ConfigureCached(cache)
		} else {
			builder.ConfigureFresh(newCache)
		}
		for generation := 0; generation < generations; generation++ {
			result, err := builder.Build(context.Background(), candidate.BuildRequest{Picker: picker,
				Location: pathutil.Filesystem([]byte(root)), Initial: generation == 0, StatWorkers: 2})
			if err != nil {
				b.Fatal(err)
			}
			if mode == "records-10000" && len(result.Records) < 10_000 {
				b.Fatalf("10,000-row fixture produced %d merged records", len(result.Records))
			}
		}
	}
	gotAttempts, gotStarts, gotExits, gotLive, gotMaxLive := counts.values()
	wantAttempts, wantStarts := attempts*b.N, starts*b.N
	wantMax := 0
	if starts > 0 {
		wantMax = 1
	}
	if gotAttempts != wantAttempts || gotStarts != wantStarts || gotExits != wantStarts || gotLive != 0 || gotMaxLive != wantMax {
		b.Fatalf("counters=%d/%d/%d/%d/%d want=%d/%d/%d/0/%d", gotAttempts, gotStarts, gotExits, gotLive,
			gotMaxLive, wantAttempts, wantStarts, wantStarts, wantMax)
	}
	b.ReportMetric(float64(gotAttempts)/float64(b.N), "zoxide-attempts/op")
	b.ReportMetric(float64(gotStarts)/float64(b.N), "zoxide-processes/op")
}

func (counts *performanceProcessCounts) values() (attempts, starts, exits, live, maxLive int) {
	counts.mu.Lock()
	defer counts.mu.Unlock()
	return counts.attempts, counts.starts, counts.exits, counts.live, counts.maxLive
}
